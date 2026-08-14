package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/evals"

	"github.com/spf13/cobra"
)

// --- evalResultLabel ---

func TestEvalResultLabel_ScenarioOnly(t *testing.T) {
	got := evalResultLabel(evals.Result{ScenarioID: "s1"})
	if got != "s1" {
		t.Fatalf("got %q, want s1", got)
	}
}

func TestEvalResultLabel_WithProvider(t *testing.T) {
	got := evalResultLabel(evals.Result{ScenarioID: "s1", Provider: "glm"})
	if got != "s1 [glm]" {
		t.Fatalf("got %q, want 's1 [glm]'", got)
	}
}

func TestEvalResultLabel_WithProviderAndModel(t *testing.T) {
	got := evalResultLabel(evals.Result{ScenarioID: "s1", Provider: "glm", Model: "glm-5.2"})
	if got != "s1 [glm/glm-5.2]" {
		t.Fatalf("got %q, want 's1 [glm/glm-5.2]'", got)
	}
}

func TestEvalResultLabel_ModelOnly(t *testing.T) {
	// Model without provider still produces a label with empty provider segment.
	got := evalResultLabel(evals.Result{ScenarioID: "s1", Model: "glm-5.2"})
	if !strings.Contains(got, "s1") || !strings.Contains(got, "glm-5.2") {
		t.Fatalf("got %q, want both scenario and model", got)
	}
}

func TestEvalResultLabel_WithFaultProfile(t *testing.T) {
	got := evalResultLabel(evals.Result{ScenarioID: "s1", Provider: "glm", Model: "glm-5.2", FaultProfile: "after-tool-429-once"})
	want := "s1 [glm/glm-5.2/fault=after-tool-429-once]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEvalResultLabel_WithEngineMode(t *testing.T) {
	got := evalResultLabel(evals.Result{ScenarioID: "s1", Provider: "glm", EngineMode: "hybrid"})
	want := "s1 [glm/engine=hybrid]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEvalResultLabel_WithTrial(t *testing.T) {
	got := evalResultLabel(evals.Result{ScenarioID: "s1", EngineMode: "auto", Trial: 2, TrialCount: 5})
	want := "s1 [engine=auto/trial=2/5]"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatREPLOperationsIsCompactAndDeterministic(t *testing.T) {
	got := formatREPLOperations(map[string]int{"search_code": 1, "count_code_many": 2})
	if got != " · operations count_code_many=2,search_code=1" {
		t.Fatalf("formatREPLOperations() = %q", got)
	}
	if got := formatREPLOperations(nil); got != "" {
		t.Fatalf("formatREPLOperations(nil) = %q, want empty", got)
	}
}

func TestEvalCommandRegistersBaselineAudit(t *testing.T) {
	cmd := newEvalCmd()
	found, _, err := cmd.Find([]string{"baseline-audit"})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.Name() != "baseline-audit" {
		t.Fatalf("baseline-audit command = %#v", found)
	}
	if found.Flags().Lookup("manifest") == nil ||
		found.Flags().Lookup("input") == nil ||
		found.Flags().Lookup("json") == nil {
		t.Fatal("baseline-audit is missing its manifest/input/json contract")
	}
}

// --- evalGateOptions (additional edge cases) ---

func TestEvalGateOptions_DisabledByDefault(t *testing.T) {
	opts, enabled, err := evalGateOptions("", "", false, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if enabled {
		t.Fatal("enabled = true, want false when no flags set")
	}
	if opts.RequireAllPassed {
		t.Fatal("RequireAllPassed = true, want false")
	}
}

func TestEvalGateOptions_RequirePassEnables(t *testing.T) {
	_, enabled, err := evalGateOptions("", "", true, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !enabled {
		t.Fatal("enabled = false, want true when requirePass=true")
	}
}

func TestEvalGateOptions_InvalidFailUnder(t *testing.T) {
	_, _, err := evalGateOptions("not-a-ratio", "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "--fail-under") {
		t.Fatalf("error = %v, want --fail-under context", err)
	}
}

func TestEvalGateOptions_InvalidMaxRegression(t *testing.T) {
	_, _, err := evalGateOptions("", "abc", false, nil)
	if err == nil || !strings.Contains(err.Error(), "--max-regression") {
		t.Fatalf("error = %v, want --max-regression context", err)
	}
}

func TestEvalGateOptions_ZeroMaxRegressionEnablesComparableBaselineGate(t *testing.T) {
	opts, enabled, err := evalGateOptions("", "0", false, nil)
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !enabled || !opts.RequireComparableBaseline || opts.MaxRegression != 0 {
		t.Fatalf("options/enabled = %+v/%v, want zero-tolerance regression gate", opts, enabled)
	}
}

func TestEvalGateOptions_FailMetricEnables(t *testing.T) {
	opts, enabled, err := evalGateOptions("", "", false, []string{"verification_passed=0.8"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if !enabled {
		t.Fatal("enabled = false, want true when metric threshold set")
	}
	if opts.MetricMinRatios["verification_passed"] != 0.8 {
		t.Fatalf("threshold = %v, want 0.8", opts.MetricMinRatios["verification_passed"])
	}
}

func TestApplyEngineGateOptions_DefaultsToAutoAndPreservesZeroLimits(t *testing.T) {
	var opts evals.GateOptions
	enabled, err := applyEngineGateOptions(&opts, evalEngineGateFlags{
		RequireCompletePairs: true, MaxScoreRegression: "0", MaxQualityRegressions: 0,
		MaxRelativeDeltas:       []string{"candidates.total_tokens=-5%"},
		MaxMedianRelativeDeltas: []string{"candidates.total_tokens=-3%"},
		MinLowerRatios:          []string{"candidates.total_tokens=67%"},
		MaxLowerPValues:         []string{"candidates.total_tokens=5%"},
		MinReplUseRatios:        []string{"candidates=50%"},
		MaxReplUseRatios:        []string{"controls=0%"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || !opts.RequireCompleteEnginePairs || len(opts.EngineModes) != 1 || opts.EngineModes[0] != "auto" {
		t.Fatalf("engine gate options = %+v enabled=%t", opts, enabled)
	}
	if opts.MaxEngineScoreRegression == nil || *opts.MaxEngineScoreRegression != 0 ||
		opts.MaxEngineQualityRegressions == nil || *opts.MaxEngineQualityRegressions != 0 {
		t.Fatalf("zero limits were not preserved: %+v", opts)
	}
	if opts.EngineMaxRelativeDeltas["candidates.total_tokens"] != -0.05 ||
		opts.EngineMaxMedianRelativeDeltas["candidates.total_tokens"] != -0.03 ||
		opts.EngineMinLowerRatios["candidates.total_tokens"] != 0.67 ||
		opts.EngineMaxLowerPValues["candidates.total_tokens"] != 0.05 {
		t.Fatalf("efficiency limits = %+v / %+v / %+v / %+v", opts.EngineMaxRelativeDeltas, opts.EngineMaxMedianRelativeDeltas, opts.EngineMinLowerRatios, opts.EngineMaxLowerPValues)
	}
	if opts.EngineMinReplUseRatios["candidates"] != 0.5 || opts.EngineMaxReplUseRatios["controls"] != 0 {
		t.Fatalf("REPL use limits = %+v / %+v", opts.EngineMinReplUseRatios, opts.EngineMaxReplUseRatios)
	}
}

func TestApplyEngineGateOptions_ValidatesInputs(t *testing.T) {
	for _, test := range []struct {
		name        string
		modes       []string
		score       string
		regressions int
	}{
		{name: "mode", modes: []string{"tools"}, regressions: -1},
		{name: "ratio", score: "invalid", regressions: -1},
		{name: "count", regressions: -2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var opts evals.GateOptions
			if _, err := applyEngineGateOptions(&opts, evalEngineGateFlags{
				Modes: test.modes, MaxScoreRegression: test.score, MaxQualityRegressions: test.regressions,
			}); err == nil {
				t.Fatalf("inputs %+v were accepted", test)
			}
		})
	}
	var opts evals.GateOptions
	if _, err := applyEngineGateOptions(&opts, evalEngineGateFlags{
		MaxQualityRegressions: -1, MinReplUseRatios: []string{"candidate=50%"},
	}); err == nil || !strings.Contains(err.Error(), "--min-engine-repl-use-ratio") {
		t.Fatalf("invalid REPL cohort error = %v", err)
	}
	if _, err := applyEngineGateOptions(&opts, evalEngineGateFlags{
		MaxQualityRegressions: -1, MaxLowerPValues: []string{"candidates.total_tokens=101%"},
	}); err == nil || !strings.Contains(err.Error(), "--max-engine-lower-p-value") {
		t.Fatalf("invalid sign-test threshold error = %v", err)
	}
	if _, err := applyEngineGateOptions(&opts, evalEngineGateFlags{
		MaxQualityRegressions: -1, MaxMedianRelativeDeltas: []string{"candidate.total_tokens=5%"},
	}); err == nil || !strings.Contains(err.Error(), "--max-engine-median-relative-delta") {
		t.Fatalf("invalid median-relative threshold error = %v", err)
	}
}

// --- printEvalReport ---

func runWithBuffer(t *testing.T, fn func(cmd *cobra.Command)) string {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	fn(cmd)
	return buf.String()
}

func TestPrintEvalReport_BasicReport(t *testing.T) {
	report := evals.Report{
		ResultsPath: "results.jsonl",
		Count:       3,
		Passed:      2,
		Failed:      1,
		Score:       evals.ScoreSummary{Passed: 4, Total: 5, Ratio: 0.8},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	if !strings.Contains(out, "results.jsonl") {
		t.Errorf("output missing results path: %q", out)
	}
	if !strings.Contains(out, "Scenarios: 3") {
		t.Errorf("output missing scenario count: %q", out)
	}
	if !strings.Contains(out, "80.0%") {
		t.Errorf("output missing score percentage: %q", out)
	}
}

func TestPrintEvalReport_WithMetrics(t *testing.T) {
	report := evals.Report{
		Count:  1,
		Passed: 1,
		Score:  evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
		Metrics: []evals.MetricSummary{
			{Name: "verification_passed", Passed: 1, Total: 1, Ratio: 1},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	if !strings.Contains(out, "Metrics:") {
		t.Errorf("output missing Metrics section: %q", out)
	}
	if !strings.Contains(out, "verification_passed") {
		t.Errorf("output missing metric name: %q", out)
	}
}

func TestPrintEvalReport_WithFailingScenarios(t *testing.T) {
	report := evals.Report{
		Count:  2,
		Passed: 1,
		Failed: 1,
		Score:  evals.ScoreSummary{Passed: 1, Total: 2, Ratio: 0.5},
		Scenarios: []evals.ScenarioSummary{
			{ID: "s1", Status: "passed"},
			{ID: "s2", Status: "failed", Error: "timeout"},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	if !strings.Contains(out, "Failing scenarios:") {
		t.Errorf("output missing Failing scenarios section: %q", out)
	}
	if !strings.Contains(out, "s2") {
		t.Errorf("output missing failing scenario id: %q", out)
	}
	if !strings.Contains(out, "timeout") {
		t.Errorf("output missing scenario error: %q", out)
	}
}

func TestPrintEvalReport_WithEngineScenarioEfficiency(t *testing.T) {
	report := evals.Report{
		Count: 2,
		Engines: []evals.EngineSummary{
			{Mode: "tools"}, {Mode: "hybrid"},
		},
		Scenarios: []evals.ScenarioSummary{
			{ID: "aggregate", Variant: "engine=tools", Status: "passed", TotalTokens: 100, ModelRounds: 2, AgentDuration: 3000},
			{ID: "aggregate", Variant: "engine=hybrid", Status: "passed", TotalTokens: 80, ModelRounds: 1, AgentDuration: 1800, ReplCalls: 1, ReplScanOperations: 1, ReplFileIndexRefreshes: 1},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	for _, want := range []string{"Scenario efficiency:", "aggregate [engine=hybrid]", "tokens 80", "rounds 1", "repl 1", "scan ops 1", "index refreshes 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestPrintEvalReport_ShowsHybridExposureFunnel(t *testing.T) {
	report := evals.Report{Engines: []evals.EngineSummary{{
		Mode: "auto", Count: 3, Passed: 3, Score: evals.ScoreSummary{Passed: 3, Total: 3, Ratio: 1},
		Efficiency: evals.EfficiencySummary{
			TrustedRuntimeScenarios: 3, MeasuredScenarios: 3, TotalTokens: 300, ModelRounds: 6,
			HybridPolicyObserved: 3, HybridModeMatched: 3,
			HybridExposureMatched: 2, HybridExposureGaps: 1,
			HybridEligible: 3, ReplExposed: 2, ReplUsedScenarios: 1, ReplCalls: 2, ReplScanOperations: 4, ReplFileIndexRefreshes: 3,
			HybridStrategies: map[string]int{"cross_file": 1, "aggregation": 2},
		},
	}}}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	for _, want := range []string{"trusted journal 3/3", "policy 3", "mode 3", "mode mismatch 0", "aligned 2", "gaps 1", "unexpected 0", "eligible 3", "exposed 2", "used 1", "repl calls 2", "scan ops 4", "index refreshes 3", "strategies aggregation=2,cross_file=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestPrintEvalReport_ShowsHybridExposureFunnelWithoutHeadlessMetrics(t *testing.T) {
	report := evals.Report{Engines: []evals.EngineSummary{{
		Mode: "auto", Count: 1, Passed: 1, Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
		Efficiency: evals.EfficiencySummary{
			TrustedRuntimeScenarios: 1,
			HybridPolicyObserved:    1, HybridModeMatched: 1, HybridExposureMatched: 1,
			HybridEligible: 1, ReplExposed: 1, ReplUsedScenarios: 1, ReplCalls: 2,
		},
	}}}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	for _, want := range []string{"trusted journal 1/1", "no headless metrics", "policy 1", "mode 1", "mode mismatch 0", "aligned 1", "gaps 0", "unexpected 0", "eligible 1", "exposed 1", "used 1", "repl calls 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestPrintEvalReport_WithPairedEngineDeltas(t *testing.T) {
	report := evals.Report{
		EngineComparisons: []evals.EngineComparison{{
			BaselineMode: "tools",
			Mode:         "auto",
			All: evals.EngineCohortComparison{
				Pairs: 2, PassedDelta: 0, ScoreDelta: 0,
				Hybrid: evals.PairedHybridFunnel{TrustedRuntime: 2, PolicyObserved: 2, ModeMatched: 2, ExposureMatched: 2, Eligible: 1, Exposed: 1, Used: 1, Calls: 1, ScanOperations: 2, FileIndexRefreshes: 2, UseRatio: 0.5},
				Efficiency: evals.PairedEfficiencyComparison{
					InputTokens:          evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 60, CurrentAverage: 48, AverageDelta: -12, Lower: 2},
					UncachedInputTokens:  evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 40, CurrentAverage: 30, AverageDelta: -10, Lower: 2},
					CacheReadInputTokens: evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 20, CurrentAverage: 18, AverageDelta: -2, Lower: 1, Equal: 1},
					OutputTokens:         evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 10, CurrentAverage: 12, AverageDelta: 2, Higher: 2},
					TotalTokens: evals.PairedMetricComparison{
						Pairs: 2, BaselineAverage: 70, CurrentAverage: 60, AverageDelta: -10, MedianDelta: -10,
						RelativeDelta: floatPtr(-1.0 / 7.0), RelativePairs: 2, MedianRelativeDelta: floatPtr(-1.0 / 7.0),
						Lower: 1, Equal: 1, EvidenceUnits: 2, ClusteredMedianDelta: -10,
						ClusteredRelativeEvidenceUnits: 2, ClusteredMedianRelativeDelta: floatPtr(-1.0 / 7.0),
						UnitLower: 1, UnitEqual: 1, LowerSignTestPValue: floatPtr(0.5),
					},
					ModelRounds:    evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 1.5, CurrentAverage: 1, AverageDelta: -0.5, RelativeDelta: floatPtr(-1.0 / 3.0), Lower: 1, Equal: 1},
					DurationMillis: evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 2000, CurrentAverage: 1400, AverageDelta: -600, RelativeDelta: floatPtr(-0.3), Lower: 1, Equal: 1},
					ReplCalls:      evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 0, CurrentAverage: 0.5, AverageDelta: 0.5, Higher: 1, Equal: 1},
					EstimatedUSD:   evals.PairedMetricComparison{Pairs: 2, BaselineAverage: 0.007, CurrentAverage: 0.0058, AverageDelta: -0.0012, RelativeDelta: floatPtr(-0.1714), Lower: 1, Equal: 1},
				},
			},
			Candidates: evals.EngineCohortComparison{Pairs: 1, Efficiency: evals.PairedEfficiencyComparison{TotalTokens: evals.PairedMetricComparison{Pairs: 1}}},
			Controls: evals.EngineCohortComparison{
				Pairs: 1, Efficiency: evals.PairedEfficiencyComparison{TotalTokens: evals.PairedMetricComparison{Pairs: 1}},
				QualityRegressions: []evals.ScenarioIdentity{{ID: "pairwise", Variant: "glm/model-a"}},
			},
			Excluded: evals.EnginePairExclusions{CurrentOnly: 1},
		}},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	for _, want := range []string{
		"Paired engine deltas vs tools", "auto", "candidates", "controls", "tokens     70.0 → 60.0",
		"avg Δ -10.0", "pair median Δ -10.0", "unit median Δ -10.0", "unit median -14.3% over 2/2 units", "pairs lower/equal/higher 1/1/0", "units 2: 1/1/0", "lower sign p=0.5", "input", "uncached", "cache read", "output", "duration", "avg Δ -0.6s", "cost USD", "avg Δ -0.0012",
		"hybrid: trusted runtime 2/2", "policy 2/2", "mode 2/2", "mode mismatch 0", "aligned 2/2", "gaps 0", "unexpected 0", "eligible 1/2", "used 1/2 (50.0%)", "scan ops 2", "index refreshes 2",
		"quality regressions", "pairwise [glm/model-a]", "current-only 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

func TestPrintEvalReport_WithFailingVariant(t *testing.T) {
	report := evals.Report{
		Count:  1,
		Failed: 1,
		Scenarios: []evals.ScenarioSummary{
			{ID: "s1", Variant: "glm", Status: "failed"},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	if !strings.Contains(out, "[glm]") {
		t.Errorf("output missing variant label: %q", out)
	}
}

func TestPrintEvalReport_WithComparison(t *testing.T) {
	report := evals.Report{Count: 1, Passed: 1, Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1}}
	cmp := evals.Comparison{
		BaselinePath: "baseline.jsonl",
		ScoreDelta:   -0.1,
		PassedDelta:  -1,
		Metrics: []evals.MetricDelta{
			{Name: "task_completed", BaselineRatio: 1, CurrentRatio: 0.8, Delta: -0.2},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, &cmp, nil)
	})
	if !strings.Contains(out, "Baseline: baseline.jsonl") {
		t.Errorf("output missing baseline path: %q", out)
	}
	if !strings.Contains(out, "Metric deltas:") {
		t.Errorf("output missing metric deltas section: %q", out)
	}
}

func TestPrintEvalReport_CohortMismatchDoesNotPrintZeroDelta(t *testing.T) {
	report := evals.Report{Count: 1, Passed: 1}
	cmp := evals.Comparison{
		BaselinePath: "baseline.jsonl",
		CohortMismatch: &evals.CohortMismatch{
			BaselineOnly: []evals.ScenarioIdentity{{ID: "a", Variant: "glm/glm-5.1"}},
			CurrentOnly:  []evals.ScenarioIdentity{{ID: "a", Variant: "glm/glm-5.2"}},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, &cmp, nil)
	})
	if !strings.Contains(out, "Comparison unavailable: cohort mismatch") || !strings.Contains(out, "[glm/glm-5.2]") {
		t.Fatalf("output missing cohort mismatch details: %q", out)
	}
	if strings.Contains(out, "Delta:") {
		t.Fatalf("invalid aggregate delta rendered for mismatched cohorts: %q", out)
	}
}

func TestPrintEvalReport_LabelsDryRunAsUnscored(t *testing.T) {
	report := evals.Report{Count: 1, DryRun: 1, Scenarios: []evals.ScenarioSummary{{ID: "a", Status: "dry_run"}}}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, nil)
	})
	if !strings.Contains(out, "dry-run: 1") {
		t.Fatalf("output = %q, want explicit dry-run count", out)
	}
}

func TestPrintEvalReport_WithGatePassed(t *testing.T) {
	report := evals.Report{Count: 1, Passed: 1, Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1}}
	gate := &evals.GateResult{Passed: true}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, gate)
	})
	if !strings.Contains(out, "Gate: passed") {
		t.Errorf("output missing Gate: passed: %q", out)
	}
}

func TestPrintEvalReport_WithGateFailed(t *testing.T) {
	report := evals.Report{Count: 1, Failed: 1, Score: evals.ScoreSummary{Passed: 0, Total: 1, Ratio: 0}}
	gate := &evals.GateResult{Passed: false, Failures: []string{"score below threshold"}}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalReport(cmd, report, nil, gate)
	})
	if !strings.Contains(out, "Gate: failed") {
		t.Errorf("output missing Gate: failed: %q", out)
	}
	if !strings.Contains(out, "score below threshold") {
		t.Errorf("output missing gate failure detail: %q", out)
	}
}

// --- printEvalDiagnosis ---

func TestPrintEvalDiagnosis_Basic(t *testing.T) {
	diag := evals.Diagnosis{
		ResultsPath: "results.jsonl",
		Score:       evals.ScoreSummary{Passed: 3, Total: 5, Ratio: 0.6},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalDiagnosis(cmd, diag)
	})
	if !strings.Contains(out, "results.jsonl") {
		t.Errorf("output missing results path: %q", out)
	}
	if !strings.Contains(out, "60.0%") {
		t.Errorf("output missing score percentage: %q", out)
	}
}

func TestPrintEvalDiagnosis_WithWeakMetrics(t *testing.T) {
	diag := evals.Diagnosis{
		Score: evals.ScoreSummary{Passed: 1, Total: 2, Ratio: 0.5},
		WeakMetrics: []evals.MetricSummary{
			{Name: "verification_passed", Passed: 1, Total: 2, Ratio: 0.5},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalDiagnosis(cmd, diag)
	})
	if !strings.Contains(out, "Weak metrics:") {
		t.Errorf("output missing Weak metrics section: %q", out)
	}
}

func TestPrintEvalDiagnosis_IncludesDuplicateAndSpecMismatchCounts(t *testing.T) {
	diag := evals.Diagnosis{CohortMismatch: &evals.CohortMismatch{
		BaselineDuplicates: []evals.ScenarioIdentity{{ID: "a"}},
		CurrentDuplicates:  []evals.ScenarioIdentity{{ID: "b"}},
		SpecMismatches:     []evals.ScenarioIdentity{{ID: "c"}},
	}}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalDiagnosis(cmd, diag)
	})
	for _, want := range []string{"1 duplicate baseline", "1 duplicate current", "1 changed spec"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
}

func TestPrintEvalDiagnosis_WithRegressions(t *testing.T) {
	diag := evals.Diagnosis{
		Score: evals.ScoreSummary{Passed: 1, Total: 2, Ratio: 0.5},
		Regressions: []evals.MetricDelta{
			{Name: "task_completed", BaselineRatio: 1, CurrentRatio: 0.5, Delta: -0.5},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalDiagnosis(cmd, diag)
	})
	if !strings.Contains(out, "Regressions:") {
		t.Errorf("output missing Regressions section: %q", out)
	}
}

func TestPrintEvalDiagnosis_WithFailedScenarios(t *testing.T) {
	diag := evals.Diagnosis{
		Score: evals.ScoreSummary{Passed: 0, Total: 1, Ratio: 0},
		FailedScenarios: []evals.ScenarioSummary{
			{ID: "s1", Variant: "glm", Status: "failed"},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalDiagnosis(cmd, diag)
	})
	if !strings.Contains(out, "Failed scenarios:") {
		t.Errorf("output missing Failed scenarios section: %q", out)
	}
	if !strings.Contains(out, "[glm]") {
		t.Errorf("output missing variant label: %q", out)
	}
}

func TestPrintEvalDiagnosis_WithRecommendations(t *testing.T) {
	diag := evals.Diagnosis{
		Score: evals.ScoreSummary{Passed: 0, Total: 1, Ratio: 0},
		Recommendations: []evals.Recommendation{
			{Area: "prompt", Reason: "low score", Action: "add examples"},
		},
	}
	out := runWithBuffer(t, func(cmd *cobra.Command) {
		printEvalDiagnosis(cmd, diag)
	})
	if !strings.Contains(out, "Recommended next actions:") {
		t.Errorf("output missing recommendations section: %q", out)
	}
	if !strings.Contains(out, "[prompt]") {
		t.Errorf("output missing area tag: %q", out)
	}
	if !strings.Contains(out, "add examples") {
		t.Errorf("output missing action text: %q", out)
	}
}

// --- Command constructors ---

func TestNewEvalCmd_HasSubcommands(t *testing.T) {
	cmd := newEvalCmd()
	subs := cmd.Commands()
	if len(subs) < 4 {
		t.Fatalf("expected at least 4 subcommands, got %d", len(subs))
	}
	seen := map[string]bool{}
	for _, c := range subs {
		seen[c.Use] = true
	}
	for _, want := range []string{"run", "report", "diagnose", "validate"} {
		if !seen[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}
}

func TestNewEvalRunCmd_FlagsRegistered(t *testing.T) {
	cmd := newEvalRunCmd()
	for _, flag := range []string{"manifest", "fixtures", "workdir", "output", "agent-command", "timeout", "dry-run", "repeat", "resume"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag %q not registered", flag)
		}
	}
}

func TestEvalRunRejectsUnsafeRepeatBeforeExecution(t *testing.T) {
	for _, value := range []string{"0", "101"} {
		cmd := newEvalRunCmd()
		cmd.SetArgs([]string{"--repeat", value, "--dry-run"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--repeat") {
			t.Fatalf("--repeat %s error = %v, want range validation", value, err)
		}
	}
}

func TestNewEvalReportCmd_FlagsRegistered(t *testing.T) {
	cmd := newEvalReportCmd()
	for _, flag := range []string{
		"input", "baseline", "json", "fail-under", "max-regression", "require-pass", "fail-metric",
		"engine-gate-mode", "require-complete-engine-pairs", "max-engine-score-regression", "max-engine-quality-regressions",
		"max-engine-relative-delta", "max-engine-median-relative-delta", "min-engine-lower-ratio", "max-engine-lower-p-value",
		"min-engine-repl-use-ratio", "max-engine-repl-use-ratio",
	} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag %q not registered", flag)
		}
	}
}

func TestNewEvalValidateCmd_FlagsRegistered(t *testing.T) {
	cmd := newEvalValidateCmd()
	for _, flag := range []string{"manifest", "fixtures", "scenario", "timeout"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag %q not registered", flag)
		}
	}
}

func TestNewEvalDiagnoseCmd_FlagsRegistered(t *testing.T) {
	cmd := newEvalDiagnoseCmd()
	for _, flag := range []string{"input", "baseline", "json"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag %q not registered", flag)
		}
	}
}

// --- Report command RunE with real JSONL ---

func TestEvalReportCmd_RunE_JSON(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")

	results := []evals.Result{
		{ScenarioID: "s1", Status: "passed", Metrics: map[string]bool{"task_completed": true}, Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1}},
	}
	data, _ := json.Marshal(results[0])
	os.WriteFile(resultsPath, append(data, '\n'), 0644)

	var buf bytes.Buffer
	cmd := newEvalReportCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--input", resultsPath, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var payload struct {
		Report evals.Report `json:"report"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if payload.Report.Count != 1 {
		t.Fatalf("report count = %d, want 1", payload.Report.Count)
	}
}

func TestEvalReportCmd_RunE_TextReport(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")

	data, _ := json.Marshal(evals.Result{ScenarioID: "s1", Status: "passed", Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1}})
	os.WriteFile(resultsPath, append(data, '\n'), 0644)

	var buf bytes.Buffer
	cmd := newEvalReportCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--input", resultsPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if !strings.Contains(buf.String(), "Scenarios:") {
		t.Errorf("output missing Scenarios line: %q", buf.String())
	}
}

func TestEvalReportCmd_RunE_MissingInput(t *testing.T) {
	cmd := newEvalReportCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--input", "/nonexistent/path/results.jsonl"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing input file")
	}
}

func TestEvalReportCmd_RunE_MaxRegressionWithoutBaseline(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")
	data, _ := json.Marshal(evals.Result{ScenarioID: "s1", Status: "passed"})
	os.WriteFile(resultsPath, append(data, '\n'), 0644)

	cmd := newEvalReportCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--input", resultsPath, "--max-regression", "5%"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--baseline") {
		t.Fatalf("error = %v, want --baseline requirement", err)
	}
}

func TestEvalReportCmd_RunE_GateFails(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")
	data, _ := json.Marshal(evals.Result{ScenarioID: "s1", Status: "failed", Score: evals.ScoreSummary{Passed: 0, Total: 1, Ratio: 0}})
	os.WriteFile(resultsPath, append(data, '\n'), 0644)

	cmd := newEvalReportCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--input", resultsPath, "--fail-under", "90%"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected gate failure error")
	}
	if !strings.Contains(err.Error(), "gate failed") {
		t.Fatalf("error = %v, want gate failure", err)
	}
}

func TestEvalReportCmd_RunE_EngineGatePassesEndToEnd(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")
	candidate := true
	results := []evals.Result{
		{
			ScenarioID: "aggregate", ScenarioSpecHash: strings.Repeat("c", 64), HybridCandidate: &candidate,
			RunSpecHash: strings.Repeat("a", 64), Provider: "p", Model: "m", EngineMode: "tools", Status: "passed",
			Score: evals.ScoreSummary{Passed: 1, Total: 1},
			Journal: &evals.JournalSummary{Path: "<eval-runtime>/execution_journal.jsonl", TrustedRuntime: true, HeadlessMetrics: &evals.HeadlessMetricsSummary{
				InputTokens: 90, OutputTokens: 10, CacheReadInputTokens: 40,
				TotalTokens: 100, TokenBreakdownTracked: true, ModelRounds: 2, DurationMillis: 2000,
			}},
		},
		{
			ScenarioID: "aggregate", ScenarioSpecHash: strings.Repeat("c", 64), HybridCandidate: &candidate,
			RunSpecHash: strings.Repeat("a", 64), Provider: "p", Model: "m", EngineMode: "auto", Status: "passed",
			Score: evals.ScoreSummary{Passed: 1, Total: 1},
			Journal: &evals.JournalSummary{
				Path:           "<eval-runtime>/execution_journal.jsonl",
				TrustedRuntime: true,
				ToolCounts:     map[string]int{"repl_exec": 1},
				HybridPolicy:   &evals.HybridPolicySummary{Mode: "auto", REPLEligible: true, REPLEnabled: true},
				HeadlessMetrics: &evals.HeadlessMetricsSummary{
					InputTokens: 70, OutputTokens: 10, CacheReadInputTokens: 30,
					TotalTokens: 80, TokenBreakdownTracked: true, ModelRounds: 1, DurationMillis: 1500,
				},
			},
		},
	}
	var data []byte
	for _, result := range results {
		line, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(resultsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	cmd := newEvalReportCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{
		"--input", resultsPath, "--json", "--require-complete-engine-pairs",
		"--max-engine-score-regression", "0", "--max-engine-quality-regressions", "0",
		"--max-engine-relative-delta", "candidates.total_tokens=-10%",
		"--max-engine-relative-delta", "candidates.input_tokens=-10%",
		"--max-engine-median-relative-delta", "candidates.total_tokens=-10%",
		"--min-engine-lower-ratio", "candidates.total_tokens=100%",
		"--max-engine-lower-p-value", "candidates.total_tokens=50%",
		"--min-engine-repl-use-ratio", "candidates=100%",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v\n%s", err, buf.String())
	}
	var payload struct {
		Gate *evals.GateResult `json:"gate"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
	if payload.Gate == nil || !payload.Gate.Passed {
		t.Fatalf("gate = %+v, want passing engine gate", payload.Gate)
	}

	// The same quality/efficiency evidence is invalid when its policy event was
	// emitted under another engine mode. Verify the public command fails closed,
	// not merely the report.go helper used by unit tests.
	results[1].Journal.HybridPolicy.Mode = "tools"
	data = data[:0]
	for _, result := range results {
		line, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(resultsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	badCmd := newEvalReportCmd()
	badCmd.SetOut(&bytes.Buffer{})
	badCmd.SetArgs([]string{"--input", resultsPath, "--json", "--require-complete-engine-pairs"})
	if err := badCmd.Execute(); err == nil || !strings.Contains(err.Error(), "gate failed") {
		t.Fatalf("wrong-mode engine gate error = %v", err)
	}
}

// --- Diagnose command RunE ---

func TestEvalDiagnoseCmd_RunE_JSON(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")
	data, _ := json.Marshal(evals.Result{ScenarioID: "s1", Status: "passed", Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1}})
	os.WriteFile(resultsPath, append(data, '\n'), 0644)

	var buf bytes.Buffer
	cmd := newEvalDiagnoseCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--input", resultsPath, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	var diag evals.Diagnosis
	if err := json.Unmarshal(buf.Bytes(), &diag); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, buf.String())
	}
}

func TestEvalDiagnoseCmd_RunE_Text(t *testing.T) {
	dir := t.TempDir()
	resultsPath := filepath.Join(dir, "results.jsonl")
	data, _ := json.Marshal(evals.Result{ScenarioID: "s1", Status: "passed", Score: evals.ScoreSummary{Passed: 1, Total: 1, Ratio: 1}})
	os.WriteFile(resultsPath, append(data, '\n'), 0644)

	var buf bytes.Buffer
	cmd := newEvalDiagnoseCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--input", resultsPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !strings.Contains(buf.String(), "Score:") {
		t.Errorf("output missing Score line: %q", buf.String())
	}
}

func TestEvalDiagnoseCmd_RunE_MissingInput(t *testing.T) {
	cmd := newEvalDiagnoseCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--input", "/nonexistent/path.jsonl"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for missing input")
	}
}

func floatPtr(value float64) *float64 {
	return &value
}
