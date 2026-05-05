package app

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gokin/internal/tools"
	"gokin/internal/ui"
)

const completionReviewDraftLimit = 700

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
	if evidence == "" || !commandsContainVerificationSignals([]string{evidence}) {
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

func shouldRunCompletionReview(userMessage, response string, toolsUsed, touchedPaths, commands []string) bool {
	if len(touchedPaths) == 0 {
		return false
	}
	if !looksLikeCodingTask(userMessage) && !doneGateTouchedPathsRequireTests(touchedPaths) {
		return false
	}
	return completionReviewNeedsAttention(response, toolsUsed, touchedPaths, commands)
}

func completionReviewNeedsAttention(response string, toolsUsed, touchedPaths, commands []string) bool {
	codeLikeTouches := doneGateTouchedPathsRequireTests(touchedPaths)
	missingReviewProof := !completionReviewHasDiffProof(toolsUsed, commands) &&
		!completionResponseMentionsTouchedPaths(response, touchedPaths)

	if !codeLikeTouches {
		return missingReviewProof
	}

	missingVerificationProof := !completionReviewHasVerificationProof(toolsUsed, commands, response)
	return missingReviewProof || missingVerificationProof
}

func completionReviewHasDiffProof(toolsUsed, commands []string) bool {
	for _, toolName := range toolsUsed {
		switch toolName {
		case "git_diff", "diff":
			return true
		}
	}

	for _, command := range commands {
		lower := " " + strings.ToLower(strings.TrimSpace(command))
		if strings.Contains(lower, " git diff") || strings.Contains(lower, " git status") || strings.Contains(lower, " git show") {
			return true
		}
	}

	return false
}

func completionReviewHasVerificationProof(toolsUsed, commands []string, response string) bool {
	for _, toolName := range toolsUsed {
		switch toolName {
		case "verify_code", "run_tests":
			return true
		}
	}
	if commandsContainVerificationSignals(commands) {
		return true
	}
	return outputContainsVerificationSignals(response)
}

func completionResponseMentionsTouchedPaths(response string, touchedPaths []string) bool {
	lower := strings.ToLower(response)
	if strings.TrimSpace(lower) == "" {
		return false
	}

	for _, path := range touchedPaths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		pathLower := strings.ToLower(path)
		if strings.Contains(lower, pathLower) {
			return true
		}
		baseLower := strings.ToLower(filepathBase(path))
		if baseLower != "" && len(baseLower) > 3 && strings.Contains(lower, baseLower) {
			return true
		}
	}
	return false
}

func buildCompletionReviewPrompt(userMessage, response string, touchedPaths, commands, gitChangedPaths, falselyClaimedPaths []string, evidenceLedger ...string) string {
	var sb strings.Builder
	sb.WriteString("Final completion review before finishing this task.\n\n")
	sb.WriteString("Original user request:\n")
	sb.WriteString(strings.TrimSpace(userMessage))
	sb.WriteString("\n\n")

	if len(evidenceLedger) > 0 {
		ledger := strings.TrimSpace(evidenceLedger[0])
		if ledger != "" {
			sb.WriteString("Runtime evidence ledger gathered this turn:\n")
			sb.WriteString(ledger)
			sb.WriteString("\n\n")
		}
	}

	if len(touchedPaths) > 0 {
		sb.WriteString("Files changed in this turn (per tool calls):\n- ")
		sb.WriteString(strings.Join(touchedPaths, "\n- "))
		sb.WriteString("\n\n")
	}
	// Ground truth from git. Distinct from touchedPaths: tools track
	// what the executor attempted, git tracks what actually landed on
	// disk after all edits settled. When these lists disagree, the
	// model is the one that's confused — surface both so it can see
	// the discrepancy rather than doubling down on its own narrative.
	if len(gitChangedPaths) > 0 {
		sb.WriteString("Files actually modified on disk (git diff --name-only + staged + untracked):\n- ")
		sb.WriteString(strings.Join(gitChangedPaths, "\n- "))
		sb.WriteString("\n\n")
	}
	if len(falselyClaimedPaths) > 0 {
		sb.WriteString("[VERIFICATION FAILED] The draft response below references these files, but they are NOT in the git working tree — either you didn't change them, the edit silently failed, or you hallucinated the path:\n- ")
		sb.WriteString(strings.Join(falselyClaimedPaths, "\n- "))
		sb.WriteString("\nRemove these claims from the final answer OR run the edit again if it was supposed to apply.\n\n")
	}
	if len(commands) > 0 {
		sb.WriteString("Successful commands already run:\n- ")
		sb.WriteString(strings.Join(commands, "\n- "))
		sb.WriteString("\n\n")
	}

	trimmedResponse := strings.TrimSpace(response)
	if trimmedResponse != "" {
		if runes := []rune(trimmedResponse); len(runes) > completionReviewDraftLimit {
			trimmedResponse = string(runes[:completionReviewDraftLimit]) + "..."
		}
		sb.WriteString("Current draft response already given to the user:\n")
		sb.WriteString(trimmedResponse)
		sb.WriteString("\n\n")
	}

	sb.WriteString("Review requirements:\n")
	sb.WriteString("- Inspect your actual changes against the request. Use git_diff or targeted reads if needed.\n")
	sb.WriteString("- If verification is missing, run the minimal relevant verification now.\n")
	sb.WriteString("- If the implementation is incomplete or inconsistent, fix it before finishing.\n")
	sb.WriteString("- Do not ask the user for clarification.\n")
	if len(gitChangedPaths) > 0 {
		// Only add the file-citation guardrail when we actually produced
		// the ground-truth list above — otherwise the reference "Files
		// actually modified on disk" points at nothing.
		sb.WriteString("- Only cite files that appear in 'Files actually modified on disk' above. Do not claim changes to files not in that list.\n")
	}
	sb.WriteString("- End with a concise completion summary that names changed files/components and verification performed.\n")
	return sb.String()
}

// detectFalselyClaimedPaths returns paths the model mentioned in its
// response that are NOT present in the actual git working-tree changes.
// Conservative: only flags paths that look unambiguous (contain "/" or
// end with a common code extension) and appear in the response. The
// runtime can't extract arbitrary prose references reliably, so this is
// a best-effort signal, not a hard gate — the model still decides what
// to do with the "[VERIFICATION FAILED]" annotation.
func detectFalselyClaimedPaths(response string, gitChanged []string) []string {
	if strings.TrimSpace(response) == "" || len(gitChanged) == 0 {
		return nil
	}
	gitSet := make(map[string]bool, len(gitChanged)*2)
	for _, p := range gitChanged {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		gitSet[p] = true
		// Also index basename so a response that writes "foo.go" counts
		// as matching "internal/app/foo.go" in the actual list.
		if idx := strings.LastIndex(p, "/"); idx >= 0 && idx+1 < len(p) {
			gitSet[p[idx+1:]] = true
		}
	}

	// Pick candidate paths from the response: tokens that look like file
	// refs. We accept tokens that either have a "/" AND a ".ext" suffix,
	// or a bare filename ending in a known code extension. Over-triggers
	// are cheaper than under-triggers here — an extra "VERIFICATION
	// FAILED" line for a non-path is visible to the model, a missed
	// hallucination propagates to the user.
	var falseClaims []string
	seen := make(map[string]bool)
	for _, field := range tokenizeResponseForPaths(response) {
		if seen[field] {
			continue
		}
		seen[field] = true
		if gitSet[field] {
			continue
		}
		// Also accept match by basename extracted from the field itself.
		if idx := strings.LastIndex(field, "/"); idx >= 0 && idx+1 < len(field) {
			if gitSet[field[idx+1:]] {
				continue
			}
		}
		falseClaims = append(falseClaims, field)
	}
	return falseClaims
}

// tokenizeResponseForPaths extracts path-like tokens from a prose
// response. Matches tokens that look like `something/file.ext` or bare
// `file.ext` with a known code extension. Keeps the regex simple — we
// only need "good enough" recall with low FP on common English prose.
var completionReviewPathToken = regexp.MustCompile(`([A-Za-z0-9_\-./]+\.(?:go|py|ts|tsx|js|jsx|rs|java|kt|rb|swift|c|cc|cpp|h|hpp|yaml|yml|toml|json|md|mod|sum))`)

func tokenizeResponseForPaths(response string) []string {
	matches := completionReviewPathToken.FindAllString(response, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		m = strings.Trim(m, ".,;:()\"'`")
		if m == "" {
			continue
		}
		out = append(out, m)
	}
	return out
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
	if !shouldRunCompletionReview(userMessage, *response, toolsUsed, touchedPaths, commands) {
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
	gitChangedPaths := discoverDoneGateTouchedPaths(a.workDir)
	falselyClaimed := detectFalselyClaimedPaths(*response, gitChangedPaths)

	ledgerSummary := a.snapshotResponseEvidence().FormatForCompletionReview()
	reviewPrompt := buildCompletionReviewPrompt(userMessage, *response, touchedPaths, commands, gitChangedPaths, falselyClaimed, ledgerSummary)
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

func filepathBase(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	if idx := strings.LastIndex(path, "\\"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}
