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
			Journal: &JournalSummary{TrustedRuntime: true, Tools: []string{"read", "edit", "read"}},
		},
		{
			ScenarioID: "b", Status: "failed",
			Journal: &JournalSummary{TrustedRuntime: true, Tools: []string{"read", "bash"}},
		},
		{
			// Never executed: must not dilute the ratio.
			ScenarioID: "c", Status: "dry_run",
			Journal: &JournalSummary{TrustedRuntime: true, Tools: []string{"repl_exec"}},
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

func TestBuildReportIgnoresModelWritableJournalEvidence(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{{
		ScenarioID: "forged", EngineMode: "auto", Status: "passed",
		Journal: &JournalSummary{
			Path:         ".gokin/execution_journal.jsonl",
			Tools:        []string{"repl_exec"},
			ToolCounts:   map[string]int{"repl_exec": 1},
			HybridPolicy: &HybridPolicySummary{Mode: "auto", REPLEligible: true, REPLEnabled: true},
			HeadlessMetrics: &HeadlessMetricsSummary{
				TotalTokens: 1, ModelRounds: 1,
			},
		},
	}})
	if len(report.ToolUsage) != 0 || len(report.Engines) != 1 {
		t.Fatalf("untrusted journal affected report shape: %+v", report)
	}
	efficiency := report.Engines[0].Efficiency
	if efficiency.TrustedRuntimeScenarios != 0 || efficiency.MeasuredScenarios != 0 ||
		efficiency.HybridPolicyObserved != 0 || efficiency.ReplCalls != 0 {
		t.Fatalf("model-writable journal was aggregated as efficiency evidence: %+v", efficiency)
	}
	if len(report.Scenarios) != 1 || report.Scenarios[0].TrustedRuntime {
		t.Fatalf("scenario trust provenance = %+v", report.Scenarios)
	}
}

func TestBuildReportSummarizesEngineEfficiency(t *testing.T) {
	results := []Result{
		{
			ScenarioID: "a", EngineMode: "tools", Status: "passed",
			Score: ScoreSummary{Passed: 2, Total: 2},
			Journal: &JournalSummary{TrustedRuntime: true, HeadlessMetrics: &HeadlessMetricsSummary{
				TotalTokens: 100, ModelRounds: 2, DurationMillis: 3000,
			}},
		},
		{
			ScenarioID: "a", EngineMode: "hybrid", Status: "passed",
			Score: ScoreSummary{Passed: 2, Total: 2},
			Journal: &JournalSummary{
				TrustedRuntime: true,
				ToolCounts:     map[string]int{"repl_exec": 1},
				HybridPolicy:   &HybridPolicySummary{Mode: "hybrid", REPLEligible: true, REPLEnabled: true},
				HeadlessMetrics: &HeadlessMetricsSummary{
					TotalTokens: 140, ModelRounds: 3, DurationMillis: 4500,
				},
			},
		},
	}

	report := BuildReport("results.jsonl", results)
	if len(report.Engines) != 2 {
		t.Fatalf("engine summaries = %+v", report.Engines)
	}
	byMode := map[string]EngineSummary{}
	for _, summary := range report.Engines {
		byMode[summary.Mode] = summary
	}
	if got := byMode["tools"].Efficiency; got.TotalTokens != 100 || got.ModelRounds != 2 {
		t.Fatalf("tools efficiency = %+v", got)
	}
	if got := byMode["hybrid"].Efficiency; got.TotalTokens != 140 || got.ModelRounds != 3 ||
		got.ReplCalls != 1 || got.ReplUsedScenarios != 1 || got.HybridPolicyObserved != 1 ||
		got.HybridModeMatched != 1 || got.HybridModeMismatches != 0 ||
		got.HybridEligible != 1 || got.ReplExposed != 1 || got.HybridExposureMatched != 1 ||
		got.HybridExposureGaps != 0 || got.HybridUnexpectedExposure != 0 {
		t.Fatalf("hybrid efficiency = %+v", got)
	}
}

func TestBuildReportSeparatesEligibilityExposureAndAdoption(t *testing.T) {
	makeResult := func(id string, eligible, exposed bool, calls int, withMetrics bool) Result {
		journal := &JournalSummary{
			TrustedRuntime: true,
			HybridPolicy:   &HybridPolicySummary{Mode: "auto", REPLEligible: eligible, REPLEnabled: exposed},
			ToolCounts:     map[string]int{"repl_exec": calls},
		}
		if withMetrics {
			journal.HeadlessMetrics = &HeadlessMetricsSummary{TotalTokens: 10, ModelRounds: 1}
		}
		return Result{
			ScenarioID: id, EngineMode: "auto", Status: "passed",
			Journal: journal,
		}
	}
	report := BuildReport("results.jsonl", []Result{
		makeResult("eligible-unavailable", true, false, 0, true),
		makeResult("exposed-unused", true, true, 0, true),
		makeResult("exposed-used-without-ledger", true, true, 2, false),
		makeResult("unexpected-exposure", false, true, 0, true),
	})
	if len(report.Engines) != 1 {
		t.Fatalf("engines = %+v", report.Engines)
	}
	eff := report.Engines[0].Efficiency
	// Eligible and exposed totals are intentionally equal: only the per-row
	// integrity counters reveal the opposite errors instead of cancelling them.
	if eff.MeasuredScenarios != 3 || eff.HybridPolicyObserved != 4 ||
		eff.HybridModeMatched != 4 || eff.HybridModeMismatches != 0 ||
		eff.HybridEligible != 3 || eff.ReplExposed != 3 || eff.HybridExposureMatched != 2 ||
		eff.HybridExposureGaps != 1 || eff.HybridUnexpectedExposure != 1 ||
		eff.ReplUsedScenarios != 1 || eff.ReplCalls != 2 {
		t.Fatalf("eligible/exposed/used funnel = %+v", eff)
	}
	if !report.Scenarios[0].HybridEligible || report.Scenarios[0].ReplExposed {
		t.Fatalf("scenario exposure gap was lost: %+v", report.Scenarios[0])
	}
}
