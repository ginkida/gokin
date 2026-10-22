package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"gokin/internal/client"
	"gokin/internal/commands"
	"gokin/internal/goals"
	"gokin/internal/hooks"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

type promptSubmissionKey struct{}
type promptExpansionKey struct{}

// A typed workflow and its expanded model prompt are one submission. A later
// automatic turn must get a fresh gate, even when headless reuses its context.
type promptSubmission struct {
	mu       sync.Mutex
	used     bool
	notes    string
	err      error
	usage    client.TokenUsageCounter
	account  *goalUsageAccount
	raw      string
	lineage  conversationLineage
	revision uint64
}

func (a *App) withPromptSubmission(ctx context.Context, raw string) (context.Context, error) {
	ctx = a.withSessionStartHooks(ctx)
	endStartupWait := a.beginSessionStartPromptWait(ctx, raw)
	startupErr := hooks.AwaitSessionStart(ctx)
	endStartupWait()
	if startupErr != nil {
		return ctx, startupErr
	}
	if a.hooksManager == nil {
		return ctx, nil
	}
	if lineage, ok := conversationLineageFromContext(ctx); ok && !a.conversationLineageMatches(lineage) {
		return ctx, &tools.HookExecutionStoppedError{Reason: "submission belongs to a replaced conversation"}
	}
	if previous, ok := ctx.Value(promptSubmissionKey{}).(*promptSubmission); ok {
		previous.mu.Lock()
		used, err := previous.used, previous.err
		previous.mu.Unlock()
		if !used {
			if !a.conversationLineageMatches(previous.lineage) {
				return ctx, fmt.Errorf("submission belongs to a replaced conversation")
			}
			if a.hooksManager != nil {
				if changed := a.hooksManager.CheckExecutionRevision(previous.revision); changed != nil {
					return ctx, changed
				}
			}
			return ctx, err
		}
	}
	state := &promptSubmission{raw: raw, lineage: a.captureConversationLineage()}
	ctx = context.WithValue(ctx, promptSubmissionKey{}, state)
	if err := ctx.Err(); err != nil {
		state.err = err
		return ctx, err
	}
	ctx = a.withAsyncHooks(ctx)
	ctx = a.withAuxiliaryRequests(ctx)
	ctx = client.ContextWithTokenUsageCounter(ctx, &state.usage)
	// Pre-expansion checks may precede observeGoalTurn. Attribute their usage
	// to an existing goal now, including a gate which blocks the submission.
	a.sessionLeaseMu.Lock()
	fields := strings.Fields(raw)
	replacesGoal := len(fields) > 0 && fields[0] == "/goal" && commands.GoalStartsWork(fields[1:])
	var goalSource *goalContinuation
	if goalObservation(ctx) == nil && !isStructuredOutputCorrection(ctx) {
		if _, automatic := ctx.Value(goalContinuationKey{}).(*goalContinuation); !automatic {
			a.goalIntent.Add(1)
			a.stopGoalTimer()
			a.replaceQueuedGoal(nil)
		}
	}
	if !replacesGoal && !isStructuredOutputCorrection(ctx) && a.session != nil {
		goal := a.session.GoalState().Current
		if goal != nil && goal.Status == goals.Active {
			if !a.goalUsage.matches(a.session.GetID(), goal) {
				a.goalUsage = &goalUsageAccount{sessionID: a.session.GetID(), goalID: goal.ID, startedAt: goal.StartedAt}
			}
			state.account = a.goalUsage
			goalSource = &goalContinuation{sessionID: a.session.GetID(), goalID: goal.ID, epoch: a.recoveryEpoch.Load(), turns: goal.Turns, intent: a.goalIntent.Load()}
		}
	}
	a.sessionLeaseMu.Unlock()
	defer func() {
		if state.err != nil && goalObservation(ctx) == nil && goalSource != nil {
			a.failGoalTurn(ctx, goalSource, state.err)
		}
	}()
	if state.account != nil {
		ctx = a.withGoalUsageContext(ctx, state.account)
	}
	defer client.CheckpointContextTokenUsage(ctx)
	if ceiling, restricted := a.toolCapabilitySnapshot(); restricted {
		if inherited, ok := tools.ToolCapabilityCeilingFromContext(ctx); ok {
			ceiling = intersectCapabilityNames(ceiling, inherited)
		}
		ctx = tools.ContextWithToolCapabilityCeiling(ctx, ceiling)
	}
	if a.executor != nil {
		if a.session != nil {
			tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
		}
		ctx = a.executor.SkillInvocationContext(ctx)
	} else {
		ctx = tools.ContextWithSkillHooks(ctx, a.registry, a.hooksManager)
	}
	info := hooks.RuntimeInfoFromContext(ctx)
	info.AgentID, info.AgentType, info.ToolNameOverride, info.ToolUseID = "", "", "", ""
	if a.session != nil {
		info.SessionID, info.SessionTitle = a.session.GetID(), a.session.GetTitle()
	}
	ctx = hooks.ContextWithRuntimeInfo(ctx, info)
	ctx = hooks.ContextWithPromptGuard(ctx, func() error {
		if !a.conversationLineageMatches(state.lineage) {
			return fmt.Errorf("submission belongs to a replaced conversation")
		}
		return nil
	})
	state.revision = a.hooksManager.ExecutionRevision()
	results := a.hooksManager.RunUserPromptSubmit(ctx, a.workDir, raw)
	if err := ctx.Err(); err != nil {
		state.err = err
		return ctx, err
	}
	if !a.conversationLineageMatches(state.lineage) {
		state.err = &tools.HookExecutionStoppedError{Reason: "submission belongs to a replaced conversation"}
		return ctx, state.err
	}
	if err := a.hooksManager.CheckExecutionRevision(state.revision); err != nil {
		state.err = &tools.HookExecutionStoppedError{Reason: err.Error()}
		return ctx, state.err
	}
	for _, result := range results {
		if result.SystemMessage != "" {
			a.safeSendToProgram(ui.StatusUpdateMsg{Type: ui.StatusInfo, Message: result.SystemMessage})
		}
		if result.StopReason != "" || result.BlockReason != "" {
			reason := result.StopReason
			if reason == "" {
				reason = result.BlockReason
			}
			if !result.SuppressOriginalPrompt {
				reason += "\n\nOriginal prompt:\n" + raw
			}
			state.err = errors.Join(&tools.HookExecutionStoppedError{Reason: reason}, result.Error)
			return ctx, state.err
		}
	}
	for _, result := range results {
		if result.Error != nil {
			a.currentPresenter().Warning(fmt.Sprintf("UserPromptSubmit hook %q failed; its output was discarded: %v", result.Hook.DisplayName(), result.Error))
			continue
		}
		if result.AdditionalContext != "" {
			state.notes += "\n[UserPromptSubmit hook context]\n" + result.AdditionalContext
		}
		if result.SessionTitle != "" && a.session != nil {
			a.sessionLeaseMu.Lock()
			if a.session.GetID() == state.lineage.sessionID && a.recoveryEpoch.Load() == state.lineage.epoch {
				a.session.SetTitle(result.SessionTitle)
			}
			a.sessionLeaseMu.Unlock()
		}
	}
	return ctx, nil
}

func (a *App) consumePromptSubmission(ctx context.Context, goalTurn *goalTurnObservation) string {
	state, _ := ctx.Value(promptSubmissionKey{}).(*promptSubmission)
	if state == nil {
		return ""
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.used {
		return ""
	}
	state.used = true
	if state.account == nil && goalTurn != nil && goalTurn.account != nil {
		// A /goal gate ran before the new goal existed. Fold just that prefix
		// into its account; future responses already share the goal context.
		input, output := state.usage.Snapshot()
		goalCtx := client.ContextWithTokenUsageCounter(context.Background(), &goalTurn.account.counter)
		client.RecordContextTokenUsage(goalCtx, input, output)
	}
	return strings.TrimSpace(state.notes)
}
