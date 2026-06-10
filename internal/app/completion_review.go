package app

// The completion-review pure core (should-run heuristics, prompt
// building, falsely-claimed-path detection) lives in internal/donegate.
// This file keeps only the App-coupled glue: per-turn command tracking
// and the review loop that re-enters the executor.

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/donegate"
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
	newHistory, reviewResponse, err := a.executor.Execute(ctx, history, reviewPrompt)
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
		a.safeSendToProgram(ui.ErrorMsg(fmt.Errorf("completion review failed: %w", err)))
		return false
	}

	a.session.SetHistory(newHistory)
	a.applyToolOutputHygiene()
	if strings.TrimSpace(reviewResponse) != "" {
		*response = reviewResponse
	}
	return true
}
