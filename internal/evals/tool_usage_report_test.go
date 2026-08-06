package evals

import "testing"

// A tool can be registered, permitted, and advertised while never being chosen.
// That is invisible in pass rates and metric ratios — the engine's Python REPL
// went unused across 42 real runs and the only way to see it was reading raw
// journals by hand. The report now answers it directly.
func TestBuildReportSummarizesWhichToolsWereChosen(t *testing.T) {
	results := []Result{
		{
			ScenarioID: "a", Status: "passed",
			Journal: &JournalSummary{Tools: []string{"read", "edit", "read"}},
		},
		{
			ScenarioID: "b", Status: "failed",
			Journal: &JournalSummary{Tools: []string{"read", "bash"}},
		},
		{
			// Never executed: must not dilute the ratio.
			ScenarioID: "c", Status: "dry_run",
			Journal: &JournalSummary{Tools: []string{"repl_exec"}},
		},
	}

	report := BuildReport("results.jsonl", results)

	byName := map[string]ToolUsageSummary{}
	for _, tool := range report.ToolUsage {
		byName[tool.Name] = tool
	}
	if len(byName) != 3 {
		t.Fatalf("tool usage = %+v, want exactly read/edit/bash", report.ToolUsage)
	}
	// Counted once per scenario, not once per call.
	if got := byName["read"]; got.Scenarios != 2 || got.Ratio != 1.0 {
		t.Fatalf("read usage = %+v, want 2 scenarios at 100%% of the 2 executed", got)
	}
	if got := byName["edit"]; got.Scenarios != 1 || got.Ratio != 0.5 {
		t.Fatalf("edit usage = %+v, want 1 of 2 executed", got)
	}
	if _, leaked := byName["repl_exec"]; leaked {
		t.Fatal("a dry-run scenario contributed tool usage")
	}
	// Most-chosen first, so an unused tool is visible by its absence.
	if report.ToolUsage[0].Name != "read" {
		t.Fatalf("tool usage not ordered by adoption: %+v", report.ToolUsage)
	}
}

// A run where nothing recorded a journal must not invent usage.
func TestBuildReportToolUsageEmptyWithoutJournals(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{{ScenarioID: "a", Status: "passed"}})
	if len(report.ToolUsage) != 0 {
		t.Fatalf("tool usage = %+v, want none", report.ToolUsage)
	}
}
