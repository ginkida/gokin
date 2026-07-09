package app

// The completion-review pure core (should-run heuristics, prompt
// building, falsely-claimed-path detection) lives in internal/donegate.
// This file keeps only the App-coupled glue: per-turn command tracking
// and the review loop that re-enters the executor.

import (
	"context"
	"errors"
	"strings"

	"gokin/internal/donegate"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

func (a *App) snapshotResponseCommands() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := make([]string, len(a.responseCommands))
	copy(snapshot, a.responseCommands)
	return snapshot
}

func (a *App) recordResponseCommand(toolName string, args map[string]any, result tools.ToolResult) {
	if !result.Success || toolName != "bash" || len(args) == 0 {
		return
	}
	command, _ := args["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.responseCommands {
		if existing == command {
			return
		}
	}
	a.responseCommands = append(a.responseCommands, command)
}

func (a *App) recordResponseVerificationEvidence(evidence string) {
	evidence = strings.TrimSpace(evidence)
	if evidence == "" || !donegate.CommandsContainVerificationSignals([]string{evidence}) {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.responseCommands {
		if existing == evidence {
			return
		}
	}
	a.responseCommands = append(a.responseCommands, evidence)
}

func (a *App) runCompletionReviewIfNeeded(
	ctx context.Context,
	userMessage string,
	response *string,
	apiInputAccum *int,
	apiOutputAccum *int,
	cacheReadAccum *int,
) bool {
	if a == nil || a.executor == nil || a.session == nil || response == nil {
		return true
	}

	toolsUsed := a.snapshotResponseToolsUsed()
	touchedPaths := a.snapshotResponseTouchedPaths()
	commands := a.snapshotResponseCommands()
	if !donegate.ShouldRunCompletionReview(userMessage, *response, toolsUsed, touchedPaths, commands) {
		return true
	}

	// If the turn was already interrupted (user Esc / deadline) before the
	// review could start, skip it — the work is done; running a model call on a
	// dead context can only fail. Returning true keeps the completed response
	// and lets the turn finalize (deliver + save) instead of discarding it.
	if ctx.Err() != nil {
		logging.Debug("completion review skipped: context already canceled; keeping completed work")
		return true
	}

	if a.program != nil {
		a.safeSendToProgram(ui.StatusUpdateMsg{
			Type:    ui.StatusInfo,
			Message: "Final self-review: checking diff and verification before completion",
			Details: map[string]any{
				"phaseLabel": "Final self-review: checking diff and verification",
			},
		})
	}

	// Ground-truth git diff for claim verification. Best-effort: if
	// discovery fails (not a git tree, git not installed, timeout) we
	// fall through to the old behaviour of re-prompting without the
	// reality check — no worse than before.
	gitChangedPaths := donegate.DiscoverTouchedPaths(a.workDir)
	falselyClaimed := donegate.DetectFalselyClaimedPaths(*response, gitChangedPaths)

	ledgerSummary := a.snapshotResponseEvidence().FormatForCompletionReview()
	reviewPrompt := donegate.BuildCompletionReviewPrompt(userMessage, *response, touchedPaths, commands, gitChangedPaths, falselyClaimed, ledgerSummary)
	history := a.session.GetHistory()
	// This Execute is an INTERNAL exchange — a user message typed during the
	// review must become the NEXT turn (pending queue), not get steered into
	// the review conversation: on this function's error path newHistory is
	// discarded, which would silently LOSE an accepted steer.
	resumeSteering := a.executor.SuspendUserSteering()
	newHistory, reviewResponse, err := a.executor.Execute(ctx, history, reviewPrompt)
	resumeSteering()
	in, out := a.executor.GetLastTokenUsage()
	_, cacheRead := a.executor.GetLastCacheMetrics()
	if apiInputAccum != nil {
		*apiInputAccum += in
	}
	if apiOutputAccum != nil {
		*apiOutputAccum += out
	}
	if cacheReadAccum != nil {
		*cacheReadAccum += cacheRead
	}
	if err != nil {
		// The completion review is a BEST-EFFORT, optional self-improvement step
		// — the HARD verification gate is enforceDoneGate, which runs next. Its
		// failure must NEVER discard the turn's real work: returning false here
		// makes the caller (message_processor) skip response delivery, session
		// save, working-memory update, AND the done-gate, so a canceled/failed
		// review would vanish minutes of completed work behind an error card
		// (field report: "general ✗ 7.1m … completion review failed: context
		// canceled"). Keep the response we already have and finalize the turn.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			logging.Debug("completion review interrupted; keeping completed work", "error", err)
		} else {
			logging.Warn("completion review could not run; keeping un-reviewed response", "error", err)
		}
		return true
	}

	a.session.SetHistory(newHistory)
	a.applyToolOutputHygiene()
	if strings.TrimSpace(reviewResponse) != "" {
		*response = reviewResponse
	}
	return true
}
