package app

// The done-gate pure core (check building, profile detection, git
// ground-truth discovery, result formatting) lives in internal/donegate
// so it can be shared by the foreground App gate and sub-agent gates.
// This file keeps only the App-coupled glue: policy from config, UI
// progress/result reporting, per-turn touched-path tracking, and the
// auto-fix loop that re-enters the executor.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gokin/internal/donegate"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

func (a *App) enforceDoneGate(ctx context.Context, userMessage string) bool {
	policy := a.doneGatePolicy()
	if !policy.Enabled {
		return true
	}

	toolsUsed := a.snapshotResponseToolsUsed()
	if !donegate.ShouldEnforce(userMessage, toolsUsed) {
		return true
	}

	profile := donegate.DetectProfileWithTouchedPaths(a.workDir, a.snapshotResponseTouchedPaths())
	checks := donegate.BuildChecks(a.registry, a.workDir, userMessage, toolsUsed, profile, policy)
	if len(checks) == 0 {
		if policy.FailClosed {
			return a.blockDoneGate("done-gate blocked finalization: no verification checks are available for this project/stack")
		}
		return true
	}

	var results []donegate.Result
	var autoFixErr error
	for attempt := 0; attempt <= policy.AutoFixAttempts; attempt++ {
		results = donegate.RunChecks(ctx, checks, policy.CheckTimeout, a.reportDoneGateProgress, a.recordResponseVerificationEvidence)
		a.reportDoneGateResults(results, attempt)

		if donegate.Passed(results) {
			a.journalEvent("done_gate_passed", map[string]any{
				"attempt": attempt,
				"checks":  len(results),
			})
			return true
		}

		if attempt >= policy.AutoFixAttempts {
			break
		}

		if err := a.runDoneGateAutoFix(ctx, userMessage, results, attempt+1, policy.AutoFixAttempts); err != nil {
			logging.Warn("done-gate auto-fix failed", "attempt", attempt+1, "error", err)
			autoFixErr = err
			break
		}
	}

	reason := "done-gate blocked finalization: required checks are still failing after auto-fix budget"
	if autoFixErr != nil {
		reason = "done-gate blocked finalization: auto-fix failed before checks passed"
	}
	if failed := donegate.FormatFailedSummary(results, 3); failed != "" {
		reason += "\nFailed checks: " + failed
	}
	return a.blockDoneGate(reason)
}

func (a *App) blockDoneGate(reason string) bool {
	a.journalEvent("done_gate_blocked", map[string]any{
		"reason": reason,
	})
	err := errors.New(reason)
	a.safeSendToProgram(ui.StreamTextMsg("\n" + err.Error() + "\n"))
	a.safeSendToProgram(ui.ErrorMsg(err))
	return false
}

func (a *App) snapshotResponseToolsUsed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := make([]string, len(a.responseToolsUsed))
	copy(snapshot, a.responseToolsUsed)
	return snapshot
}

func (a *App) snapshotResponseTouchedPaths() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := make([]string, len(a.responseTouchedPaths))
	copy(snapshot, a.responseTouchedPaths)
	return snapshot
}

func (a *App) recordResponseTouchedPaths(toolName string, args map[string]any, result tools.ToolResult) {
	if !result.Success || !donegate.IsMutationTool(toolName) {
		return
	}

	paths := donegate.NormalizeTouchedPaths(a.workDir, donegate.ExtractTouchedPaths(args))
	if len(paths) == 0 {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	seen := make(map[string]bool, len(a.responseTouchedPaths))
	for _, existing := range a.responseTouchedPaths {
		seen[existing] = true
	}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		a.responseTouchedPaths = append(a.responseTouchedPaths, path)
	}
	sort.Strings(a.responseTouchedPaths)
}

func (a *App) doneGatePolicy() donegate.Policy {
	p := donegate.Policy{
		Enabled:         true,
		Mode:            "strict",
		FailClosed:      true,
		CheckTimeout:    donegate.DefaultCheckTimeout,
		AutoFixAttempts: 2,
	}
	if a.config == nil {
		return p
	}

	cfg := a.config.DoneGate
	p.Enabled = cfg.Enabled
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "normal", "strict":
		p.Mode = mode
	case "":
		// Keep strict default.
	default:
		logging.Warn("invalid done-gate mode; using strict", "mode", cfg.Mode)
	}
	p.FailClosed = cfg.FailClosed
	if cfg.CheckTimeout > 0 {
		p.CheckTimeout = cfg.CheckTimeout
	}
	if cfg.AutoFixAttempts >= 0 {
		p.AutoFixAttempts = cfg.AutoFixAttempts
	}
	return p
}

func (a *App) reportDoneGateProgress(current, total int, name string) {
	if a == nil || total <= 0 {
		return
	}
	label := fmt.Sprintf("Quality gate %d/%d: %s", current, total, name)
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusInfo,
		Message: label,
		Details: map[string]any{
			"phaseLabel": label,
			"silent":     true,
		},
	})
}

func (a *App) reportDoneGateResults(results []donegate.Result, attempt int) {
	var sb strings.Builder
	passed, failed := donegate.CountResults(results)
	if attempt == 0 {
		fmt.Fprintf(&sb, "\nQuality gate: %s\n", formatDoneGateResultSummary(passed, failed))
	} else {
		fmt.Fprintf(&sb, "\nQuality gate recheck after auto-fix #%d: %s\n", attempt, formatDoneGateResultSummary(passed, failed))
	}

	for _, r := range results {
		status := "PASS"
		if !r.Success {
			status = "FAIL"
		}
		fmt.Fprintf(&sb, "- %s: %s\n", donegate.ResultDisplayName(r), status)
		if !r.Success {
			if detail := compactDoneGateFailureDetail(r); detail != "" {
				sb.WriteString("  -> ")
				sb.WriteString(detail)
				sb.WriteString("\n")
			}
		}
	}

	a.safeSendToProgram(ui.StreamTextMsg(sb.String()))
}

func formatDoneGateResultSummary(passed, failed int) string {
	switch {
	case failed == 0:
		return fmt.Sprintf("%d passed", passed)
	case passed == 0:
		return fmt.Sprintf("%d failed", failed)
	default:
		return fmt.Sprintf("%d passed, %d failed", passed, failed)
	}
}

func (a *App) runDoneGateAutoFix(ctx context.Context, userMessage string, results []donegate.Result, attempt, max int) error {
	var failed []donegate.Result
	for _, r := range results {
		if !r.Success {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		return nil
	}

	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("\n🔧 Auto-fix attempt %d/%d — resolving %d incomplete check(s)...\n", attempt, max, len(failed))))
	label := fmt.Sprintf("Auto-fix %d/%d: resolving %d failed quality check(s)", attempt, max, len(failed))
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusInfo,
		Message: label,
		Details: map[string]any{
			"phaseLabel": label,
			"silent":     true,
		},
	})

	fixPrompt := donegate.BuildFixPrompt(userMessage, failed, attempt, max)
	history := a.session.GetHistory()

	newHistory, _, err := a.executor.Execute(ctx, history, fixPrompt)
	if err != nil {
		a.safeSendToProgram(ui.StreamTextMsg(
			fmt.Sprintf("   Auto-fix attempt %d failed: %s\n", attempt, err.Error())))
		return err
	}

	a.session.SetHistory(newHistory)
	a.applyToolOutputHygiene()
	if a.sessionManager != nil {
		_ = a.sessionManager.SaveAfterMessage()
	}

	a.safeSendToProgram(ui.StreamTextMsg(
		fmt.Sprintf("   Auto-fix attempt %d complete.\n", attempt)))
	return nil
}

// compactDoneGateFailureDetail mirrors the unexported helper of the same
// name in internal/donegate — kept here because reportDoneGateResults
// (App-coupled UI rendering) needs it too.
func compactDoneGateFailureDetail(r donegate.Result) string {
	raw := strings.TrimSpace(r.Error)
	if raw == "" {
		raw = strings.TrimSpace(r.Content)
	}
	if raw == "" {
		return ""
	}

	raw = strings.Join(strings.Fields(raw), " ")
	if raw == "" {
		return ""
	}
	if runes := []rune(raw); len(runes) > 280 {
		return string(runes[:280]) + "..."
	}
	return raw
}

// shouldSkipDoneGateDir and pathDepth mirror the unexported helpers of
// the same names in internal/donegate — kept here because
// project_detector.go's project-map walk reuses the same skip/depth
// semantics.

func shouldSkipDoneGateDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".next", ".turbo",
		".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache", ".idea", ".vscode", ".gokin-donegate-build":
		return true
	default:
		return false
	}
}

func pathDepth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

// shellQuote mirrors the unexported helper of the same name in
// internal/donegate — kept here because message_processor.go's plan-step
// verify-command wrapping uses it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
