package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/fileutil"
	"gokin/internal/goals"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

type goalContinuation struct {
	sessionID, goalID string
	epoch             uint64
	prompt            string
	turns             int
	intent            uint64
	scheduled         bool
	evaluateOnly      bool
	checkIn           bool
	generated         bool
	checkpoints       []tools.ToolCheckpoint
}
type goalContinuationKey struct{}

func (a *App) goalPolicyError() error {
	if a.hooksManager == nil {
		return fmt.Errorf("goals require an enabled hooks manager and a trusted workspace")
	}
	return a.hooksManager.GoalPolicyError()
}

// Caller holds sessionLeaseMu. Goal publication has the same mandatory-save
// rollback boundary as other executable session metadata.
func (a *App) saveGoalState(state goals.State) (err error) {
	if account := a.goalUsage; account.matches(a.session.GetID(), state.Current) {
		input, output := account.counter.Snapshot()
		state = goals.Clone(state)
		state.Current.InputTokens += max(input-account.input, 0)
		state.Current.OutputTokens += max(output-account.output, 0)
		if account.matches(a.session.GetID(), state.LastAchieved) {
			state.LastAchieved.InputTokens = state.Current.InputTokens
			state.LastAchieved.OutputTokens = state.Current.OutputTokens
		}
		defer func() {
			if err == nil || errors.Is(err, fileutil.ErrAtomicWriteCommitUncertain) {
				account.input, account.output = input, output
			}
		}()
	}
	if !a.sessionManager.PersistenceEnabled() {
		return a.session.SetGoalState(state)
	}
	id := a.session.GetID()
	if a.sessionLease == nil {
		lease, err := chat.AcquireSessionWriterLease(id)
		if err != nil {
			return err
		}
		a.sessionLease = lease
	}
	if !a.sessionLease.IsActive() || a.sessionLease.SessionID() != id {
		return fmt.Errorf("goal does not own the session writer lease")
	}
	return a.sessionManager.ApplyAndSave(func() (func(), error) {
		before := a.session.GoalState()
		if err := a.session.SetGoalState(state); err != nil {
			return nil, err
		}
		return func() { _ = a.session.SetGoalState(before) }, nil
	})
}

func (a *App) SetGoal(ctx context.Context, condition string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.goalPolicyError(); err != nil {
		return err
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	state, err := goals.Set(a.session.GoalState(), condition, time.Now())
	if err != nil {
		return err
	}
	if err := a.saveGoalState(state); err != nil {
		return err
	}
	a.stopGoalTimer()
	a.goalMu.Lock()
	a.goalNext = nil
	a.goalWaiting = nil
	a.goalMu.Unlock()
	return nil
}

func (a *App) ClearGoal(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	state := a.session.GoalState()
	if state.Current == nil || state.Current.Status != goals.Active {
		return "No goal set.", nil
	}
	condition := state.Current.Condition
	if err := a.saveGoalState(goals.Clear(state, "Cleared by the user", time.Now())); err != nil {
		return "", err
	}
	a.stopGoalTimer()
	a.goalMu.Lock()
	a.goalNext = nil
	a.goalWaiting = nil
	a.goalMu.Unlock()
	a.replaceQueuedGoal(nil)
	return "Goal cleared: " + condition, nil
}

func (a *App) GoalStatus() string {
	state := a.session.GoalState()
	goal := state.Current
	if goal == nil || goal.Status == goals.Cleared {
		goal = state.LastAchieved
	}
	if goal == nil {
		return "No goal set."
	}
	elapsed := time.Since(goal.StartedAt)
	if !goal.FinishedAt.IsZero() {
		elapsed = goal.FinishedAt.Sub(goal.StartedAt)
	}
	status := string(goal.Status)
	if goal.Suspended && goal.Status == goals.Active {
		if !goal.RetryAt.IsZero() {
			status += " (retry scheduled)"
		} else {
			status += " (paused)"
		}
	}
	return fmt.Sprintf("Goal %s: %s\nElapsed: %s · Evaluated turns: %d · Tokens: %d\nLatest reason: %s", status, goal.Condition, elapsed.Round(time.Second), goal.Turns, goal.InputTokens+goal.OutputTokens, goal.Reason)
}

func (a *App) takeGoalContinuation() *goalContinuation {
	a.goalMu.Lock()
	next := a.goalNext
	a.goalNext = nil
	a.goalMu.Unlock()
	if next == nil {
		return nil
	}
	goal := a.session.GoalState().Current
	if goal == nil || goal.ID != next.goalID || goal.Status != goals.Active || goal.Suspended || goal.Turns != next.turns || a.session.GetID() != next.sessionID || a.recoveryEpoch.Load() != next.epoch {
		return nil
	}
	return next
}

func (a *App) finishGoalTurn(ctx context.Context, response string, worker *turnUsageAccumulator) {
	if isStructuredOutputCorrection(ctx) {
		return
	}
	if deferred := deferredGoalTurn(ctx); deferred != nil {
		deferred.observation, deferred.worker, deferred.ready = goalObservation(ctx), worker, true
		if deferred.observation != nil {
			deferred.observation.finished = true
		}
		return
	}
	a.sessionLeaseMu.Lock()
	if observation := goalObservation(ctx); observation != nil {
		if !a.goalSourceMatchesLocked(&observation.source) {
			a.sessionLeaseMu.Unlock()
			return
		}
	}
	state := a.session.GoalState()
	goal := state.Current
	lineage := conversationLineage{sessionID: a.session.GetID(), epoch: a.recoveryEpoch.Load()}
	a.sessionLeaseMu.Unlock()
	if goal == nil || goal.Status != goals.Active || ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	headlessTurn := a.headlessDirect
	a.mu.Unlock()
	if headlessTurn && !a.headlessPipelineHealthy(ctx) {
		return
	}
	if observation := goalObservation(ctx); observation != nil {
		observation.finished = true
	}
	if err := a.goalPolicyError(); err != nil {
		a.suspendGoal(ctx, goal.ID, err.Error())
		return
	}
	if running := a.runningGoalTasks(); len(running) > 0 {
		a.waitForGoalBackground(ctx, goal, lineage, running)
		return
	}
	before := a.session.GetState()
	if observation := goalObservation(ctx); observation != nil {
		observation.evaluating = true
	}
	verdict, input, output, err := a.evaluateGoal(ctx, goal, before.History)
	if err != nil {
		if observation := goalObservation(ctx); observation != nil {
			observation.input, observation.output = input, output
		}
		source := &goalContinuation{sessionID: lineage.sessionID, epoch: lineage.epoch, goalID: goal.ID, turns: goal.Turns, intent: a.goalIntent.Load()}
		if observation := goalObservation(ctx); observation != nil {
			source = &observation.source
		}
		a.failGoalTurn(ctx, source, err)
		a.reportHeadlessStopFailure(ctx, a.activeHeadlessTerminalToken(), err)
		return
	}
	a.sessionLeaseMu.Lock()
	current := a.session.GoalState()
	if a.session.GetID() != lineage.sessionID || a.recoveryEpoch.Load() != lineage.epoch || current.Current == nil || current.Current.ID != goal.ID || current.Current.Status != goals.Active || ctx.Err() != nil || a.session.GetVersion() != before.Version {
		a.sessionLeaseMu.Unlock()
		return
	}
	if running := a.runningGoalTasks(); len(running) > 0 {
		a.sessionLeaseMu.Unlock()
		if observation := goalObservation(ctx); observation != nil {
			observation.input, observation.output = input, output
		}
		a.waitForGoalBackground(ctx, goal, lineage, running)
		return
	}
	if err := a.goalPolicyError(); err != nil {
		a.sessionLeaseMu.Unlock()
		a.suspendGoal(ctx, goal.ID, err.Error())
		return
	}
	usedTools := len(a.snapshotResponseToolsUsed()) > 0
	workerInput, workerOutput := goalWorkerUsage(ctx, worker)
	input += workerInput
	output += workerOutput
	updated, err := goals.Apply(current, verdict, usedTools, input, output, time.Now())
	if err == nil {
		err = a.saveGoalState(updated)
	}
	if err != nil {
		a.sessionLeaseMu.Unlock()
		a.recordHeadlessTerminalOutcome("persistence_failed", err.Error())
		a.safeSendToProgram(ui.ErrorMsg(err))
		return
	}
	goal = updated.Current
	a.stopGoalTimer()
	next := &goalContinuation{sessionID: lineage.sessionID, epoch: lineage.epoch, goalID: goal.ID, prompt: goals.ContinuationPrompt(goal), turns: goal.Turns, generated: true}
	a.mu.Lock()
	headless := a.headlessDirect
	a.mu.Unlock()
	if headless && (goal.Suspended || goal.Status == goals.Impossible) {
		a.recordHeadlessTerminalOutcome("goal_unfinished", goal.Reason)
	}
	if goal.Status == goals.Active && !goal.Suspended {
		if headless {
			a.goalMu.Lock()
			a.goalNext = next
			a.goalMu.Unlock()
		} else {
			a.replaceQueuedGoal(next)
		}
	} else {
		a.replaceQueuedGoal(nil)
	}
	a.sessionLeaseMu.Unlock()
	a.journalEvent("goal_verdict", map[string]any{"goal_id": goal.ID, "verdict": verdict.Verdict, "reason": verdict.Reason})
	a.safeSendToProgram(ui.StreamTextMsg("\n" + a.GoalStatus() + "\n"))
	if goal.Status != goals.Active || goal.Suspended {
		// The worker already sent ResponseDone before the verdict's text.
		a.safeSendToProgram(ui.ResponseDoneMsg{})
	}
}

// User input always wins. Replace an older automatic assessment rather than
// letting it run with stale guidance after a user-steered turn.
func (a *App) replaceQueuedGoal(next *goalContinuation) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	var kept []pendingRequest
	for _, request := range a.pendingQueue {
		if request.goal == nil {
			kept = append(kept, request)
		}
	}
	if len(kept) == 0 && next != nil {
		kept = append(kept, pendingRequest{message: next.prompt, goal: next})
	}
	a.pendingQueue = kept
}

func (a *App) prependInitialGoal(prompt string, submission ...*promptSubmission) {
	goal := a.session.GoalState().Current
	if goal == nil || goal.Status != goals.Active {
		return
	}
	next := &goalContinuation{sessionID: a.session.GetID(), goalID: goal.ID, epoch: a.recoveryEpoch.Load(), prompt: prompt, turns: goal.Turns}
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	request := pendingRequest{message: prompt, goal: next}
	if len(submission) > 0 {
		request.promptSubmission = submission[0]
	}
	a.pendingQueue = append([]pendingRequest{request}, a.pendingQueue...)
}

func (a *App) prepareGoalTurn(ctx context.Context) error {
	if isStructuredOutputCorrection(ctx) {
		return nil
	}
	if _, continuation := ctx.Value(goalContinuationKey{}).(*goalContinuation); continuation {
		return nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	a.goalMu.Lock()
	a.goalWaiting = nil
	a.goalMu.Unlock()
	state := a.session.GoalState()
	if state.Current == nil || state.Current.Status != goals.Active || !state.Current.Suspended && state.Current.NoProgressTurns == 0 && state.Current.RetryAttempts == 0 && state.Current.IdleCheckIns == 0 {
		return nil
	}
	state.Current.Suspended, state.Current.NoProgressTurns = false, 0
	state.Current.RetryAttempts, state.Current.IdleCheckIns = 0, 0
	state.Current.RetryAt = time.Time{}
	return a.saveGoalState(state)
}

func (a *App) suspendGoal(ctx context.Context, id, reason string) {
	// Cancellation prevents additional work, but saving a local pause must not
	// inherit an expired model deadline. No external action is performed here.
	a.sessionLeaseMu.Lock()
	state := a.session.GoalState()
	if state.Current == nil || state.Current.ID != id || state.Current.Status != goals.Active {
		a.sessionLeaseMu.Unlock()
		return
	}
	state.Current.Suspended, state.Current.Reason = true, truncateRunesApp(reason, 4000)
	state.Current.RetryAt = time.Time{}
	a.stopGoalTimer()
	if err := a.saveGoalState(state); err != nil {
		a.recordHeadlessTerminalOutcome("persistence_failed", err.Error())
	}
	a.sessionLeaseMu.Unlock()
	a.safeSendToProgram(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: "Goal paused: " + reason})
}

func (a *App) goalCommand(ctx context.Context, prompt string) (work, result string, handled bool, err error) {
	fields := strings.Fields(prompt)
	if len(fields) == 0 || fields[0] != "/goal" {
		return prompt, "", false, nil
	}
	if len(fields) == 1 {
		return "", a.GoalStatus(), true, nil
	}
	if len(fields) == 2 {
		switch strings.ToLower(fields[1]) {
		case "clear", "stop", "off", "reset", "none", "cancel":
			result, err := a.ClearGoal(ctx)
			return "", result, true, err
		}
	}
	condition := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(prompt), "/goal"))
	if err := a.SetGoal(ctx, condition); err != nil {
		return "", "", true, err
	}
	return goals.InitialPrompt(condition), "", true, nil
}
