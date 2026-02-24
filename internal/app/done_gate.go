package app

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

const (
	doneGateMaxAutoFixAttempts = 2
	doneGateOutputLimit        = 1600
)

type doneGateCheck struct {
	Name string
	Run  func(context.Context) (tools.ToolResult, error)
}

type doneGateResult struct {
	Name    string
	Success bool
	Content string
	Error   string
}

func (a *App) enforceDoneGate(ctx context.Context, userMessage string) {
	toolsUsed := a.snapshotResponseToolsUsed()
	if !shouldEnforceDoneGate(userMessage, toolsUsed) {
		return
	}

	checks := a.buildDoneGateChecks(userMessage, toolsUsed)
	if len(checks) == 0 {
		return
	}

	var results []doneGateResult
	for attempt := 0; attempt <= doneGateMaxAutoFixAttempts; attempt++ {
		results = a.runDoneGateChecks(ctx, checks)
		a.reportDoneGateResults(results, attempt)

		if doneGatePassed(results) {
			a.journalEvent("done_gate_passed", map[string]any{
				"attempt": attempt,
				"checks":  len(results),
			})
			return
		}

		if attempt >= doneGateMaxAutoFixAttempts {
			break
		}

		if err := a.runDoneGateAutoFix(ctx, userMessage, results, attempt+1, doneGateMaxAutoFixAttempts); err != nil {
			logging.Warn("done-gate auto-fix failed", "attempt", attempt+1, "error", err)
			break
		}
	}

	a.journalEvent("done_gate_blocked", map[string]any{
		"checks": len(results),
	})
	a.safeSendToProgram(ui.StreamTextMsg(
		"\nDone-gate blocked final response: required checks are still failing after auto-fix budget.\n"))
}

func (a *App) snapshotResponseToolsUsed() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	snapshot := make([]string, len(a.responseToolsUsed))
	copy(snapshot, a.responseToolsUsed)
	return snapshot
}

func (a *App) buildDoneGateChecks(userMessage string, toolsUsed []string) []doneGateCheck {
	var checks []doneGateCheck

	if verifyTool, ok := a.registry.Get("verify_code"); ok {
		checks = append(checks, doneGateCheck{
			Name: "verify_code",
			Run: func(ctx context.Context) (tools.ToolResult, error) {
				return verifyTool.Execute(ctx, map[string]any{"path": a.workDir})
			},
		})
	}

	if shouldRunDoneGateTests(userMessage, toolsUsed) {
		if testsTool, ok := a.registry.Get("run_tests"); ok {
			checks = append(checks, doneGateCheck{
				Name: "run_tests",
				Run: func(ctx context.Context) (tools.ToolResult, error) {
					return testsTool.Execute(ctx, map[string]any{"path": a.workDir, "framework": "auto"})
				},
			})
		}
	}

	return checks
}

func (a *App) runDoneGateChecks(ctx context.Context, checks []doneGateCheck) []doneGateResult {
	results := make([]doneGateResult, 0, len(checks))
	for _, check := range checks {
		result, err := check.Run(ctx)
		if err != nil {
			results = append(results, doneGateResult{
				Name:    check.Name,
				Success: false,
				Error:   err.Error(),
			})
			continue
		}
		results = append(results, doneGateResult{
			Name:    check.Name,
			Success: result.Success,
			Content: strings.TrimSpace(result.Content),
			Error:   strings.TrimSpace(result.Error),
		})
	}
	return results
}

func (a *App) reportDoneGateResults(results []doneGateResult, attempt int) {
	var sb strings.Builder
	if attempt == 0 {
		sb.WriteString("\nDone-gate checks:\n")
	} else {
		sb.WriteString(fmt.Sprintf("\nDone-gate recheck after auto-fix #%d:\n", attempt))
	}

	for _, r := range results {
		status := "PASS"
		if !r.Success {
			status = "FAIL"
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", r.Name, status))
	}

	a.safeSendToProgram(ui.StreamTextMsg(sb.String()))
}

func (a *App) runDoneGateAutoFix(ctx context.Context, userMessage string, results []doneGateResult, attempt, max int) error {
	var failed []doneGateResult
	for _, r := range results {
		if !r.Success {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		return nil
	}

	fixPrompt := buildDoneGateFixPrompt(userMessage, failed, attempt, max)
	history := a.session.GetHistory()

	newHistory, _, err := a.executor.Execute(ctx, history, fixPrompt)
	if err != nil {
		return err
	}

	a.session.SetHistory(newHistory)
	a.applyToolOutputHygiene()
	if a.sessionManager != nil {
		_ = a.sessionManager.SaveAfterMessage()
	}
	return nil
}

func doneGatePassed(results []doneGateResult) bool {
	for _, r := range results {
		if !r.Success {
			return false
		}
	}
	return true
}

func shouldEnforceDoneGate(userMessage string, toolsUsed []string) bool {
	if len(toolsUsed) == 0 {
		return false
	}

	sideEffecting := map[string]bool{
		"write": true, "edit": true, "move": true, "copy": true, "delete": true,
		"mkdir": true, "refactor": true, "batch": true,
	}

	for _, name := range toolsUsed {
		if sideEffecting[name] {
			return true
		}
		if name == "bash" && looksLikeCodingTask(userMessage) {
			return true
		}
	}
	return false
}

func shouldRunDoneGateTests(userMessage string, toolsUsed []string) bool {
	for _, name := range toolsUsed {
		if name == "run_tests" {
			return true
		}
	}

	lower := strings.ToLower(userMessage)
	triggers := []string{
		"run tests", "go test", "pytest", "npm test", "cargo test",
		"запусти тест", "прогони тест", "прогон тест", "падение тест",
	}
	for _, t := range triggers {
		if strings.Contains(lower, t) {
			return true
		}
	}
	return false
}

func looksLikeCodingTask(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	if lower == "" {
		return false
	}
	keywords := []string{
		"implement", "fix", "refactor", "update", "change", "bug", "build", "lint", "compile",
		"доработ", "исправ", "рефактор", "обнов", "помен", "ошиб", "сборк", "линт", "код",
	}
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func buildDoneGateFixPrompt(userMessage string, failed []doneGateResult, attempt, max int) string {
	var sb strings.Builder
	sb.WriteString("The hard done-gate failed. Autonomously fix the code until checks pass.\n")
	sb.WriteString(fmt.Sprintf("Auto-fix attempt %d/%d.\n\n", attempt, max))
	sb.WriteString("Original user task:\n")
	sb.WriteString(userMessage)
	sb.WriteString("\n\nFailed checks:\n")
	for _, r := range failed {
		sb.WriteString(fmt.Sprintf("- %s failed.\n", r.Name))
		if r.Error != "" {
			sb.WriteString("  Error: ")
			sb.WriteString(truncateDoneGateText(r.Error))
			sb.WriteString("\n")
		}
		if r.Content != "" {
			sb.WriteString("  Output: ")
			sb.WriteString(truncateDoneGateText(r.Content))
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nRules:\n")
	sb.WriteString("- Apply minimal deterministic fixes.\n")
	sb.WriteString("- Do not ask the user for input.\n")
	sb.WriteString("- After fixing, stop with a short summary of changes.\n")
	return sb.String()
}

func truncateDoneGateText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= doneGateOutputLimit {
		return s
	}
	return s[:doneGateOutputLimit] + "..."
}
