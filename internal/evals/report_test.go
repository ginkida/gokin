package evals

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestBuildReport_AggregatesScoresAndMetrics(t *testing.T) {
	results := []Result{
		{
			ScenarioID: "a",
			Status:     "passed",
			Provider:   "kimi",
			Metrics: map[string]bool{
				"task_completed":      true,
				"verification_passed": true,
			},
			Score: ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
		},
		{
			ScenarioID: "b",
			Status:     "failed",
			Metrics: map[string]bool{
				"task_completed":      true,
				"verification_passed": false,
			},
			Score: ScoreSummary{Passed: 1, Total: 2, Ratio: 0.5},
			Error: "verification failed",
		},
	}

	report := BuildReport("results.jsonl", results)
	if report.Count != 2 || report.Passed != 1 || report.Failed != 1 {
		t.Fatalf("counts = %+v, want count=2 passed=1 failed=1", report)
	}
	if report.Score.Passed != 3 || report.Score.Total != 4 || report.Score.Ratio != 0.75 {
		t.Fatalf("score = %+v, want 3/4 0.75", report.Score)
	}
	if len(report.Metrics) != 2 {
		t.Fatalf("metrics = %d, want 2", len(report.Metrics))
	}
	gotVerification := metricByName(report.Metrics, "verification_passed")
	if gotVerification.Passed != 1 || gotVerification.Total != 2 || gotVerification.Ratio != 0.5 {
		t.Fatalf("verification metric = %+v, want 1/2 0.5", gotVerification)
	}
	if report.Scenarios[0].Status != "failed" {
		t.Fatalf("first scenario = %+v, want failed scenarios sorted first", report.Scenarios[0])
	}
}

func TestBuildReport_DryRunIsExplicitAndExcludedFromResults(t *testing.T) {
	results := []Result{
		{
			ScenarioID: "dry",
			Status:     "dry_run",
			// Legacy dry-run result files claimed this synthetic success.
			Metrics: map[string]bool{"task_completed": true},
			Score:   ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
		},
		{
			ScenarioID: "executed",
			Status:     "passed",
			Metrics:    map[string]bool{"task_completed": false},
			Score:      ScoreSummary{Passed: 0, Total: 1, Ratio: 0},
		},
	}

	report := BuildReport("results.jsonl", results)
	if report.Count != 2 || report.Passed != 1 || report.DryRun != 1 || report.Failed != 0 {
		t.Fatalf("counts = %+v, want count=2 passed=1 dry_run=1 failed=0", report)
	}
	if report.Score.Passed != 0 || report.Score.Total != 1 || report.Score.Ratio != 0 {
		t.Fatalf("score = %+v, want only the executed result to be scored", report.Score)
	}
	metric := metricByName(report.Metrics, "task_completed")
	if metric.Passed != 0 || metric.Total != 1 || metric.Ratio != 0 {
		t.Fatalf("task_completed metric = %+v, want only the executed result", metric)
	}
	for _, scenario := range report.Scenarios {
		if scenario.Status == "dry_run" && scenario.Score.Total != 0 {
			t.Fatalf("dry-run scenario score = %+v, want zero", scenario.Score)
		}
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"dry_run":1`) {
		t.Fatalf("report JSON = %s, want explicit dry_run count", encoded)
	}

	gate := EvaluateGate(report, nil, GateOptions{RequireAllPassed: true})
	if gate.Passed || !gateFailureContains(gate, "dry-run") {
		t.Fatalf("gate = %+v, want require-pass to reject dry-run results", gate)
	}
}

func TestBuildReport_NonExecutedStatusCannotContributeSyntheticScore(t *testing.T) {
	report := BuildReport("legacy.jsonl", []Result{{
		ScenarioID: "setup",
		Status:     "setup_failed",
		Metrics:    map[string]bool{"task_completed": true},
		Score:      ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
	}})
	if report.Failed != 1 || report.Score != (ScoreSummary{}) || len(report.Metrics) != 0 {
		t.Fatalf("report = %+v, want failed but entirely unscored setup result", report)
	}
	if len(report.Scenarios) != 1 || report.Scenarios[0].Score != (ScoreSummary{}) {
		t.Fatalf("scenario score = %+v, want synthetic score removed", report.Scenarios)
	}
	gate := EvaluateGate(report, nil, GateOptions{MinScoreRatio: 0.1})
	if gate.Passed || !gateFailureContains(gate, "score") {
		t.Fatalf("gate = %+v, want fail-under to reject non-measurement", gate)
	}
}

func TestBuildReport_EngineComparisonsArePairedAndCohortSafe(t *testing.T) {
	candidate, control := true, false
	measured := func(tokens, rounds int, duration int64, replCalls int) *JournalSummary {
		outputTokens := min(tokens, 10)
		inputTokens := tokens - outputTokens
		cacheReadTokens := inputTokens / 2
		return &JournalSummary{
			TrustedRuntime: true, ToolCounts: map[string]int{"repl_exec": replCalls},
			HeadlessMetrics: &HeadlessMetricsSummary{
				InputTokens: inputTokens, OutputTokens: outputTokens, CacheReadInputTokens: cacheReadTokens,
				TotalTokens: tokens, TokenBreakdownTracked: true, ModelRounds: rounds, DurationMillis: duration,
				EstimatedUSD: float64(tokens) / 10000, CostTracked: true,
			},
		}
	}
	result := func(id, provider, mode, status string, classification *bool, passed, tokens, rounds int, duration int64, repl int) Result {
		return Result{
			ScenarioID: id, Provider: provider, Model: "model-a", EngineMode: mode,
			ScenarioSpecHash: strings.Repeat("c", 64), RunSpecHash: strings.Repeat("a", 64),
			HybridCandidate: classification, Status: status,
			Score: ScoreSummary{Passed: passed, Total: 2}, Journal: measured(tokens, rounds, duration, repl),
		}
	}
	results := []Result{
		result("aggregate", "provider-a", "tools", "passed", &candidate, 2, 100, 2, 3000, 0),
		result("aggregate", "provider-a", "auto", "passed", &candidate, 2, 80, 1, 1800, 1),
		result("aggregate", "provider-a", "hybrid", "failed", &candidate, 1, 140, 3, 4500, 1),
		result("pairwise", "provider-a", "tools", "passed", &control, 2, 40, 1, 1000, 0),
		result("pairwise", "provider-a", "auto", "passed", &control, 2, 40, 1, 1000, 0),
		result("pairwise", "provider-a", "hybrid", "passed", &control, 2, 70, 2, 2000, 1),
		// Same scenario/model but another provider: pairing must not cross it.
		result("aggregate", "provider-b", "auto", "passed", &candidate, 2, 1, 1, 1, 0),
		// A baseline row without either target remains explicitly unpaired.
		result("tools-only", "provider-a", "tools", "passed", &control, 2, 10, 1, 500, 0),
	}

	report := BuildReport("results.jsonl", results)
	if len(report.EngineComparisons) != 2 {
		t.Fatalf("engine comparisons = %+v, want auto and hybrid", report.EngineComparisons)
	}
	byMode := make(map[string]EngineComparison)
	for _, comparison := range report.EngineComparisons {
		byMode[comparison.Mode] = comparison
	}

	auto := byMode["auto"]
	if auto.All.Pairs != 2 || auto.Candidates.Pairs != 1 || auto.Controls.Pairs != 1 {
		t.Fatalf("auto pairing = %+v, want 2 all / 1 candidate / 1 control", auto)
	}
	if auto.All.PassedDelta != 0 || auto.All.ScoreDelta != 0 || auto.All.Efficiency.TotalTokens.Pairs != 2 {
		t.Fatalf("auto quality = %+v, want equal quality over two measured pairs", auto.All)
	}
	autoEfficiency := auto.All.Efficiency
	if autoEfficiency.TotalTokens.AverageDelta != -10 || autoEfficiency.ModelRounds.AverageDelta != -0.5 ||
		autoEfficiency.DurationMillis.AverageDelta != -600 || autoEfficiency.ReplCalls.AverageDelta != 0.5 ||
		autoEfficiency.EstimatedUSD.Pairs != 2 || math.Abs(autoEfficiency.EstimatedUSD.AverageDelta-(-0.001)) > 1e-12 ||
		autoEfficiency.TotalTokens.Lower != 1 || autoEfficiency.TotalTokens.Equal != 1 || autoEfficiency.TotalTokens.Higher != 0 ||
		autoEfficiency.TotalTokens.RelativeDelta == nil || math.Abs(*autoEfficiency.TotalTokens.RelativeDelta-(-1.0/7.0)) > 1e-12 ||
		autoEfficiency.ReplCalls.RelativeDelta != nil {
		t.Fatalf("auto efficiency deltas = %+v", auto.All)
	}
	if autoEfficiency.InputTokens.Pairs != 2 || autoEfficiency.InputTokens.AverageDelta != -10 ||
		autoEfficiency.OutputTokens.AverageDelta != 0 || autoEfficiency.CacheReadInputTokens.AverageDelta != -5 ||
		autoEfficiency.UncachedInputTokens.AverageDelta != -5 {
		t.Fatalf("auto token breakdown deltas = %+v", autoEfficiency)
	}
	if auto.Excluded.CurrentOnly != 1 || auto.Excluded.BaselineOnly != 1 {
		t.Fatalf("auto exclusions = %+v, want provider-b current-only and tools-only baseline", auto.Excluded)
	}

	hybrid := byMode["hybrid"]
	if hybrid.All.Pairs != 2 || hybrid.All.PassedDelta != -1 || hybrid.All.ScoreDelta != -0.25 {
		t.Fatalf("hybrid quality = %+v, want one regression and -25pp", hybrid.All)
	}
	hybridEfficiency := hybrid.All.Efficiency
	if hybridEfficiency.TotalTokens.AverageDelta != 35 || hybridEfficiency.ModelRounds.AverageDelta != 1 ||
		hybridEfficiency.DurationMillis.AverageDelta != 1250 || hybridEfficiency.ReplCalls.AverageDelta != 1 ||
		hybridEfficiency.EstimatedUSD.Pairs != 2 || math.Abs(hybridEfficiency.EstimatedUSD.AverageDelta-0.0035) > 1e-12 ||
		hybridEfficiency.TotalTokens.Higher != 2 || hybridEfficiency.TotalTokens.RelativeDelta == nil ||
		math.Abs(*hybridEfficiency.TotalTokens.RelativeDelta-0.5) > 1e-12 {
		t.Fatalf("hybrid efficiency deltas = %+v", hybrid.All)
	}
	if len(hybrid.All.QualityRegressions) != 1 || hybrid.All.QualityRegressions[0] != (ScenarioIdentity{ID: "aggregate", Variant: "provider-a/model-a"}) {
		t.Fatalf("hybrid regressions = %+v", hybrid.All.QualityRegressions)
	}
	if hybrid.Candidates.ScoreDelta != -0.5 || hybrid.Controls.ScoreDelta != 0 {
		t.Fatalf("hybrid candidate/control split = candidate %+v control %+v", hybrid.Candidates, hybrid.Controls)
	}
}

func TestEvaluateGate_TokenBreakdownMetricFailsClosedWithoutTrackedComponents(t *testing.T) {
	relative := -0.2
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto",
		Provenance: verifiedEnginePairProvenance(1),
		All: EngineCohortComparison{Pairs: 1, Efficiency: PairedEfficiencyComparison{
			TotalTokens: PairedMetricComparison{Pairs: 1, RelativeDelta: &relative},
		}},
		Candidates: EngineCohortComparison{Pairs: 1, Efficiency: PairedEfficiencyComparison{
			TotalTokens: PairedMetricComparison{Pairs: 1, RelativeDelta: &relative},
		}},
	}}}
	gate := EvaluateGate(report, nil, GateOptions{
		EngineModes:             []string{"auto"},
		EngineMaxRelativeDeltas: map[string]float64{"candidates.input_tokens": 0},
	})
	if gate.Passed || !gateFailureContains(gate, "no paired efficiency evidence") {
		t.Fatalf("component gate = %+v, want missing token breakdown rejected", gate)
	}
}

func TestBuildReport_TokenBreakdownRequiresExplicitConsistentProvenance(t *testing.T) {
	candidate := true
	result := func(mode string, input, output, cache, total int, tracked bool) Result {
		return Result{
			ScenarioID: "aggregate", Provider: "p", Model: "m", EngineMode: mode,
			ScenarioSpecHash: "same", HybridCandidate: &candidate, Status: "passed",
			Score: ScoreSummary{Passed: 1, Total: 1},
			Journal: &JournalSummary{TrustedRuntime: true, HeadlessMetrics: &HeadlessMetricsSummary{
				InputTokens: input, OutputTokens: output, CacheReadInputTokens: cache,
				TotalTokens: total, TokenBreakdownTracked: tracked,
			}},
		}
	}

	comparison := func(results []Result) PairedEfficiencyComparison {
		report := BuildReport("results.jsonl", results)
		if len(report.EngineComparisons) != 1 {
			t.Fatalf("engine comparisons = %+v, want one", report.EngineComparisons)
		}
		return report.EngineComparisons[0].Candidates.Efficiency
	}
	legacy := comparison([]Result{
		result("tools", 90, 10, 40, 100, false),
		result("auto", 70, 10, 30, 80, false),
	})
	if legacy.TotalTokens.Pairs != 1 || legacy.InputTokens.Pairs != 0 {
		t.Fatalf("legacy metrics = %+v, want total only", legacy)
	}
	inconsistent := comparison([]Result{
		result("tools", 90, 10, 40, 99, true),
		result("auto", 70, 10, 30, 80, true),
	})
	if inconsistent.InputTokens.Pairs != 0 {
		t.Fatalf("inconsistent breakdown accepted: %+v", inconsistent)
	}
	valid := comparison([]Result{
		result("tools", 90, 10, 40, 100, true),
		result("auto", 70, 10, 30, 80, true),
	})
	if valid.InputTokens.Pairs != 1 || valid.UncachedInputTokens.AverageDelta != -10 ||
		valid.CacheReadInputTokens.AverageDelta != -10 || valid.OutputTokens.AverageDelta != 0 {
		t.Fatalf("valid breakdown metrics = %+v", valid)
	}
}

func TestPairedMetricComparisonClustersRepeatedTrialsForSignTest(t *testing.T) {
	var acc pairedMetricAccumulator
	for range 3 {
		acc.add("scenario-a\x00provider\x00model", 100, 90)
		acc.add("scenario-b\x00provider\x00model", 100, 80)
	}
	metric := acc.finalize()
	if metric.Pairs != 6 || metric.Lower != 6 || metric.EvidenceUnits != 2 ||
		metric.UnitLower != 2 || metric.UnitEqual != 0 || metric.UnitHigher != 0 {
		t.Fatalf("clustered direction counts = %+v", metric)
	}
	if metric.LowerSignTestPValue == nil || math.Abs(*metric.LowerSignTestPValue-0.25) > 1e-12 {
		t.Fatalf("clustered sign p = %+v, want 0.25 from two units (not 1/64 from six trials)", metric.LowerSignTestPValue)
	}
	if metric.MedianDelta != -15 || metric.RelativePairs != 6 || metric.MedianRelativeDelta == nil ||
		math.Abs(*metric.MedianRelativeDelta-(-0.15)) > 1e-12 || metric.ClusteredMedianDelta != -15 ||
		metric.ClusteredRelativeEvidenceUnits != 2 || metric.ClusteredMedianRelativeDelta == nil ||
		math.Abs(*metric.ClusteredMedianRelativeDelta-(-0.15)) > 1e-12 {
		t.Fatalf("robust paired deltas = %+v", metric)
	}
}

func TestPairedMetricComparisonClustersMedianEffectAcrossRepeatedTrials(t *testing.T) {
	var acc pairedMetricAccumulator
	for _, current := range []float64{10, 10, 200} {
		acc.add("volatile-scenario", 100, current)
	}
	for range 3 {
		acc.add("stable-scenario", 100, 110)
	}
	metric := acc.finalize()
	if metric.MedianRelativeDelta == nil || math.Abs(*metric.MedianRelativeDelta-0.10) > 1e-12 {
		t.Fatalf("pair median = %+v, want +10%%", metric.MedianRelativeDelta)
	}
	wantClustered := (-80.0/300.0 + 0.10) / 2
	if metric.ClusteredMedianRelativeDelta == nil ||
		math.Abs(*metric.ClusteredMedianRelativeDelta-wantClustered) > 1e-12 {
		t.Fatalf("clustered median = %+v, want %.6f", metric.ClusteredMedianRelativeDelta, wantClustered)
	}

	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(6), All: EngineCohortComparison{Pairs: 6},
		Candidates: EngineCohortComparison{Pairs: 6, Efficiency: PairedEfficiencyComparison{TotalTokens: metric}},
	}}}
	passing := EvaluateGate(report, nil, GateOptions{
		EngineMaxMedianRelativeDeltas: map[string]float64{"candidates.total_tokens": -0.05},
	})
	if !passing.Passed {
		t.Fatalf("clustered median gate = %+v, want pass despite raw pair median", passing)
	}
}

func TestPairedMetricComparisonSignTestExcludesTiedUnits(t *testing.T) {
	var acc pairedMetricAccumulator
	acc.add("tie", 100, 90)
	acc.add("tie", 100, 110)
	acc.add("lower", 100, 90)
	acc.add("higher", 100, 110)
	metric := acc.finalize()
	if metric.EvidenceUnits != 3 || metric.UnitLower != 1 || metric.UnitEqual != 1 || metric.UnitHigher != 1 {
		t.Fatalf("unit direction counts = %+v", metric)
	}
	if metric.LowerSignTestPValue == nil || math.Abs(*metric.LowerSignTestPValue-0.75) > 1e-12 {
		t.Fatalf("sign p = %+v, want P[Binomial(2,.5)>=1]=0.75", metric.LowerSignTestPValue)
	}
}

func TestExactBinomialUpperTail(t *testing.T) {
	for _, test := range []struct {
		trials, successes int
		want              float64
	}{
		{trials: 6, successes: 6, want: 1.0 / 64},
		{trials: 6, successes: 5, want: 7.0 / 64},
		{trials: 6, successes: 3, want: 42.0 / 64},
		{trials: 6, successes: 0, want: 1},
	} {
		if got := exactBinomialUpperTail(test.trials, test.successes); math.Abs(got-test.want) > 1e-12 {
			t.Errorf("exactBinomialUpperTail(%d, %d) = %.15g, want %.15g", test.trials, test.successes, got, test.want)
		}
	}
	if !math.IsNaN(exactBinomialUpperTail(2, 3)) {
		t.Fatal("invalid binomial parameters must return NaN")
	}
}

func TestBuildReport_EngineComparisonRejectsInvalidPairs(t *testing.T) {
	candidate, control := true, false
	results := []Result{
		{ScenarioID: "dry", EngineMode: "tools", Status: "dry_run", HybridCandidate: &candidate},
		{ScenarioID: "dry", EngineMode: "auto", Status: "passed", HybridCandidate: &candidate},
		{ScenarioID: "spec", EngineMode: "tools", Status: "passed", ScenarioSpecHash: "a", HybridCandidate: &candidate},
		{ScenarioID: "spec", EngineMode: "auto", Status: "passed", ScenarioSpecHash: "b", HybridCandidate: &candidate},
		{ScenarioID: "run-spec", EngineMode: "tools", Status: "passed", RunSpecHash: strings.Repeat("a", 64), HybridCandidate: &candidate},
		{ScenarioID: "run-spec", EngineMode: "auto", Status: "passed", RunSpecHash: strings.Repeat("b", 64), HybridCandidate: &candidate},
		{ScenarioID: "class", EngineMode: "tools", Status: "passed", HybridCandidate: &candidate},
		{ScenarioID: "class", EngineMode: "auto", Status: "passed", HybridCandidate: &control},
		{ScenarioID: "duplicate", EngineMode: "tools", Status: "passed", HybridCandidate: &candidate},
		{ScenarioID: "duplicate", EngineMode: "tools", Status: "passed", HybridCandidate: &candidate},
		{ScenarioID: "duplicate", EngineMode: "auto", Status: "passed", HybridCandidate: &candidate},
	}
	report := BuildReport("results.jsonl", results)
	if len(report.EngineComparisons) != 1 {
		t.Fatalf("comparisons = %+v, want auto comparison", report.EngineComparisons)
	}
	comparison := report.EngineComparisons[0]
	if comparison.All.Pairs != 0 || comparison.Excluded.NonExecuted != 1 || comparison.Excluded.SpecMismatches != 1 ||
		comparison.Excluded.RunSpecMismatches != 1 || comparison.Excluded.ClassificationMismatches != 1 || comparison.Excluded.DuplicateCohorts != 1 {
		t.Fatalf("comparison = %+v, want every invalid cohort rejected with provenance", comparison)
	}
}

func TestBuildReport_EngineComparisonPairsWithinTrial(t *testing.T) {
	candidate := true
	measured := func(tokens int) *JournalSummary {
		return &JournalSummary{TrustedRuntime: true, HeadlessMetrics: &HeadlessMetricsSummary{TotalTokens: tokens}}
	}
	results := []Result{
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "tools", Trial: 1, TrialCount: 2, Status: "passed", HybridCandidate: &candidate, Journal: measured(100)},
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "auto", Trial: 1, TrialCount: 2, Status: "passed", HybridCandidate: &candidate, Journal: measured(90)},
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "tools", Trial: 2, TrialCount: 2, Status: "passed", HybridCandidate: &candidate, Journal: measured(100)},
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "auto", Trial: 2, TrialCount: 2, Status: "passed", HybridCandidate: &candidate, Journal: measured(80)},
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "auto", Trial: 3, TrialCount: 3, Status: "passed", HybridCandidate: &candidate, Journal: measured(1)},
	}
	report := BuildReport("results.jsonl", results)
	if len(report.EngineComparisons) != 1 {
		t.Fatalf("comparisons = %+v", report.EngineComparisons)
	}
	comparison := report.EngineComparisons[0]
	if comparison.All.Pairs != 2 || comparison.Excluded.CurrentOnly != 1 || comparison.Excluded.DuplicateCohorts != 0 {
		t.Fatalf("trial pairing = %+v, want two pairs and isolated unmatched trial 3", comparison)
	}
	metric := comparison.All.Efficiency.TotalTokens
	if metric.Pairs != 2 || metric.EvidenceUnits != 1 || metric.UnitLower != 1 ||
		metric.LowerSignTestPValue == nil || math.Abs(*metric.LowerSignTestPValue-0.5) > 1e-12 {
		t.Fatalf("trial-clustered metric = %+v, want two pairs but one independent evidence unit", metric)
	}
}

func TestEvaluateGate_EnginePairProvenanceRejectsLegacyAndForgedHashes(t *testing.T) {
	candidate := true
	result := func(mode, scenarioHash, runHash string) Result {
		return Result{
			ScenarioID: "a", Provider: "p", Model: "m", EngineMode: mode,
			ScenarioSpecHash: scenarioHash, RunSpecHash: runHash,
			Status: "passed", HybridCandidate: &candidate,
		}
	}
	for _, test := range []struct {
		name         string
		scenarioHash string
		runHash      string
		classified   bool
		want         string
	}{
		{name: "missing both", want: "verified scenario specification provenance for 0/1"},
		{name: "forged strings", scenarioHash: "same", runHash: "same", want: "verified scenario specification provenance for 0/1"},
		{name: "missing run", scenarioHash: strings.Repeat("c", 64), classified: true, want: "verified run specification provenance for 0/1"},
		{name: "missing classification", scenarioHash: strings.Repeat("c", 64), runHash: strings.Repeat("a", 64), want: "verified candidate/control classification for 0/1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseline := result("tools", test.scenarioHash, test.runHash)
			current := result("auto", test.scenarioHash, test.runHash)
			if !test.classified {
				baseline.HybridCandidate = nil
				current.HybridCandidate = nil
			}
			report := BuildReport("legacy.jsonl", []Result{baseline, current})
			if len(report.EngineComparisons) != 1 || report.EngineComparisons[0].All.Pairs != 1 {
				t.Fatalf("diagnostic comparison = %+v, want one visible legacy pair", report.EngineComparisons)
			}
			gate := EvaluateGate(report, nil, GateOptions{
				EngineModes:             []string{"auto"},
				EngineMaxRelativeDeltas: map[string]float64{"all.total_tokens": 0},
			})
			if gate.Passed || !gateFailureContains(gate, test.want) {
				t.Fatalf("gate = %+v, want provenance failure %q", gate, test.want)
			}
		})
	}
}

func TestBuildReport_PairedHybridFunnelUsesExactCohortDenominator(t *testing.T) {
	candidate, control := true, false
	result := func(id, mode string, classification *bool, eligible, exposed bool, calls int) Result {
		return Result{
			ScenarioID: id, Provider: "p", Model: "m", EngineMode: mode,
			HybridCandidate: classification, Status: "passed",
			Journal: &JournalSummary{
				TrustedRuntime: true,
				HybridPolicy:   &HybridPolicySummary{Mode: mode, REPLEligible: eligible, REPLEnabled: exposed},
				ToolCounts:     map[string]int{"repl_exec": calls},
			},
		}
	}
	var results []Result
	for index, calls := range []int{1, 0, 2} {
		id := fmt.Sprintf("candidate-%d", index)
		results = append(results,
			result(id, "tools", &candidate, false, false, 0),
			result(id, "auto", &candidate, true, true, calls),
		)
	}
	results = append(results,
		result("control", "tools", &control, false, false, 0),
		result("control", "auto", &control, false, false, 0),
	)
	report := BuildReport("results.jsonl", results)
	if len(report.EngineComparisons) != 1 {
		t.Fatalf("comparisons = %+v", report.EngineComparisons)
	}
	comparison := report.EngineComparisons[0]
	funnel := comparison.Candidates.Hybrid
	if comparison.Candidates.Pairs != 3 || funnel.PolicyObserved != 3 || funnel.ModeMatched != 3 ||
		funnel.ModeMismatches != 0 || funnel.Eligible != 3 ||
		funnel.ExposureMatched != 3 || funnel.ExposureGaps != 0 || funnel.UnexpectedExposure != 0 ||
		funnel.Exposed != 3 || funnel.Used != 2 || funnel.Calls != 3 ||
		math.Abs(funnel.UseRatio-2.0/3.0) > 1e-12 {
		t.Fatalf("candidate funnel = %+v over %+v", funnel, comparison.Candidates)
	}
	if controls := comparison.Controls.Hybrid; comparison.Controls.Pairs != 1 || controls.ModeMatched != 1 ||
		controls.ExposureMatched != 1 ||
		controls.Used != 0 || controls.UseRatio != 0 {
		t.Fatalf("control funnel = %+v", controls)
	}
}

func TestBuildReport_TracksRuntimeREPLOperationsAndEfficientPath(t *testing.T) {
	candidate := true
	result := func(mode string, efficient bool, operations map[string]int) Result {
		metrics := map[string]bool{}
		if mode != "tools" {
			metrics["hybrid_efficient_path"] = efficient
		}
		return Result{
			ScenarioID: "batched", Provider: "p", Model: "m", EngineMode: mode,
			HybridCandidate: &candidate, Status: "passed",
			Metrics: metrics,
			Score:   ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
			Journal: &JournalSummary{
				TrustedRuntime: true,
				ToolCounts:     map[string]int{"repl_exec": 1}, ReplOperations: operations,
				ReplFileIndexRefreshes: replScanOperationCount(operations),
				HybridPolicy: &HybridPolicySummary{
					Mode: mode, Strategy: map[bool]string{true: "aggregation"}[mode != "tools"],
					REPLEligible: mode != "tools", REPLEnabled: mode != "tools",
				},
			},
		}
	}
	report := BuildReport("results.jsonl", []Result{
		result("tools", true, nil),
		result("auto", true, map[string]int{"count_code_many": 1, "list_files": 2}),
	})
	if len(report.Engines) != 2 {
		t.Fatalf("engines = %+v", report.Engines)
	}
	var auto EngineSummary
	for _, engine := range report.Engines {
		if engine.Mode == "auto" {
			auto = engine
		}
	}
	if auto.Efficiency.EfficientPathExpected != 1 || auto.Efficiency.EfficientPathMatched != 1 ||
		auto.Efficiency.EfficientPathMisses != 0 || auto.Efficiency.ReplOperations["count_code_many"] != 1 ||
		auto.Efficiency.ReplOperations["list_files"] != 2 || auto.Efficiency.ReplScanOperations != 3 ||
		auto.Efficiency.ReplFileIndexRefreshes != 3 || auto.Efficiency.HybridStrategies["aggregation"] != 1 {
		t.Fatalf("auto efficiency = %+v", auto.Efficiency)
	}
	if len(report.EngineComparisons) != 1 {
		t.Fatalf("comparisons = %+v", report.EngineComparisons)
	}
	funnel := report.EngineComparisons[0].Candidates.Hybrid
	if funnel.EfficientExpected != 1 || funnel.EfficientMatched != 1 || funnel.EfficientMisses != 0 ||
		funnel.ScanOperations != 3 || funnel.FileIndexRefreshes != 3 || funnel.Strategies["aggregation"] != 1 {
		t.Fatalf("paired efficient-path funnel = %+v", funnel)
	}
	for _, scenario := range report.Scenarios {
		if scenario.Variant == "p/m/engine=auto" &&
			(scenario.ReplOperations["list_files"] != 2 || scenario.ReplScanOperations != 3 ||
				scenario.ReplFileIndexRefreshes != 3 || scenario.HybridStrategy != "aggregation") {
			t.Fatalf("scenario REPL operations = %#v, scans=%d, refreshes=%d",
				scenario.ReplOperations, scenario.ReplScanOperations, scenario.ReplFileIndexRefreshes)
		}
	}
}

func TestBuildReport_PairedHybridFunnelDoesNotCancelOppositeExposureErrors(t *testing.T) {
	candidate := true
	result := func(id, mode string, eligible, exposed bool) Result {
		return Result{
			ScenarioID: id, Provider: "p", Model: "m", EngineMode: mode,
			HybridCandidate: &candidate, Status: "passed",
			Journal: &JournalSummary{TrustedRuntime: true, HybridPolicy: &HybridPolicySummary{
				Mode: mode, REPLEligible: eligible, REPLEnabled: exposed,
			}},
		}
	}
	report := BuildReport("results.jsonl", []Result{
		result("availability-gap", "tools", false, false),
		result("availability-gap", "auto", true, false),
		result("policy-leak", "tools", false, false),
		result("policy-leak", "auto", false, true),
	})
	if len(report.EngineComparisons) != 1 {
		t.Fatalf("comparisons = %+v", report.EngineComparisons)
	}
	funnel := report.EngineComparisons[0].All.Hybrid
	if funnel.PolicyObserved != 2 || funnel.Eligible != 1 || funnel.Exposed != 1 ||
		funnel.ModeMatched != 2 || funnel.ModeMismatches != 0 || funnel.ExposureMatched != 0 ||
		funnel.ExposureGaps != 1 || funnel.UnexpectedExposure != 1 {
		t.Fatalf("cross-cancelling exposure errors were hidden: %+v", funnel)
	}
}

func TestBuildReport_PairedHybridFunnelTracksWrongModeProvenance(t *testing.T) {
	candidate := true
	report := BuildReport("results.jsonl", []Result{
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "tools", HybridCandidate: &candidate,
			Status: "passed", Journal: &JournalSummary{TrustedRuntime: true, HybridPolicy: &HybridPolicySummary{Mode: "tools"}}},
		{ScenarioID: "a", Provider: "p", Model: "m", EngineMode: "auto", HybridCandidate: &candidate,
			Status: "passed", Journal: &JournalSummary{TrustedRuntime: true, HybridPolicy: &HybridPolicySummary{Mode: "tools"}}},
	})
	if len(report.EngineComparisons) != 1 {
		t.Fatalf("comparisons = %+v", report.EngineComparisons)
	}
	funnel := report.EngineComparisons[0].All.Hybrid
	if funnel.PolicyObserved != 1 || funnel.ModeMatched != 0 || funnel.ModeMismatches != 1 ||
		funnel.ExposureMatched != 1 {
		t.Fatalf("wrong-mode policy provenance = %+v", funnel)
	}
}

func TestCompareReports_ComputesMetricAndScoreDeltas(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{
		ScenarioID: "a",
		Status:     "failed",
		Metrics: map[string]bool{
			"task_completed":      true,
			"verification_passed": false,
		},
		Score: ScoreSummary{Passed: 1, Total: 2, Ratio: 0.5},
	}})
	current := BuildReport("current.jsonl", []Result{{
		ScenarioID: "a",
		Status:     "passed",
		Metrics: map[string]bool{
			"task_completed":      true,
			"verification_passed": true,
		},
		Score: ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
	}})

	cmp := CompareReports(baseline, current)
	if cmp.PassedDelta != 1 {
		t.Fatalf("PassedDelta = %d, want 1", cmp.PassedDelta)
	}
	if cmp.ScoreDelta != 0.5 {
		t.Fatalf("ScoreDelta = %v, want 0.5", cmp.ScoreDelta)
	}
	verification := metricDeltaByName(cmp.Metrics, "verification_passed")
	if verification.Delta != 1 {
		t.Fatalf("verification delta = %+v, want +1", verification)
	}
	if len(cmp.Scenarios) != 1 || cmp.Scenarios[0].BaselineStatus != "failed" || cmp.Scenarios[0].CurrentStatus != "passed" {
		t.Fatalf("scenario diff = %+v, want failed -> passed", cmp.Scenarios)
	}
}

func TestCompareReports_CohortVariantMismatchFailsClosed(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{
		ScenarioID: "a",
		Provider:   "glm",
		Model:      "glm-5.2",
		Status:     "passed",
		Metrics:    map[string]bool{"verification_passed": true},
		Score:      ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
	}})
	current := BuildReport("current.jsonl", []Result{{
		ScenarioID: "a",
		Provider:   "glm",
		Model:      "glm-5.1",
		Status:     "failed",
		Metrics:    map[string]bool{"verification_passed": false},
		Score:      ScoreSummary{Passed: 0, Total: 1, Ratio: 0},
	}})

	cmp := CompareReports(baseline, current)
	if cmp.CohortMismatch == nil {
		t.Fatal("CohortMismatch = nil, want variant mismatch")
	}
	if len(cmp.CohortMismatch.BaselineOnly) != 1 || cmp.CohortMismatch.BaselineOnly[0] != (ScenarioIdentity{ID: "a", Variant: "glm/glm-5.2"}) {
		t.Fatalf("baseline-only cohort = %+v", cmp.CohortMismatch.BaselineOnly)
	}
	if len(cmp.CohortMismatch.CurrentOnly) != 1 || cmp.CohortMismatch.CurrentOnly[0] != (ScenarioIdentity{ID: "a", Variant: "glm/glm-5.1"}) {
		t.Fatalf("current-only cohort = %+v", cmp.CohortMismatch.CurrentOnly)
	}
	if cmp.ScoreDelta != 0 || cmp.PassedDelta != 0 || len(cmp.Metrics) != 0 || len(cmp.Scenarios) != 0 {
		t.Fatalf("non-comparable deltas = %+v, want aggregate/metric/scenario deltas suppressed", cmp)
	}

	gate := EvaluateGate(current, &cmp, GateOptions{MaxRegression: 0.1, RequireComparableBaseline: true})
	if gate.Passed || !gateFailureContains(gate, "cohort mismatch") {
		t.Fatalf("gate = %+v, want cohort mismatch failure", gate)
	}
}

func TestEvaluateGate_FailsScoreMetricsAndRegressions(t *testing.T) {
	report := BuildReport("current.jsonl", []Result{
		{
			ScenarioID: "a",
			Status:     "passed",
			Metrics: map[string]bool{
				"task_completed":      true,
				"verification_passed": false,
			},
			Score: ScoreSummary{Passed: 1, Total: 2, Ratio: 0.5},
		},
		{
			ScenarioID: "b",
			Status:     "failed",
			Metrics: map[string]bool{
				"task_completed":      false,
				"verification_passed": false,
			},
			Score: ScoreSummary{Passed: 0, Total: 2, Ratio: 0},
		},
	})
	baseline := BuildReport("baseline.jsonl", []Result{
		{
			ScenarioID: "a",
			Status:     "passed",
			Metrics: map[string]bool{
				"task_completed":      true,
				"verification_passed": true,
			},
			Score: ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
		},
		{
			ScenarioID: "b",
			Status:     "passed",
			Metrics: map[string]bool{
				"task_completed":      true,
				"verification_passed": true,
			},
			Score: ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
		},
	})
	cmp := CompareReports(baseline, report)

	gate := EvaluateGate(report, &cmp, GateOptions{
		MinScoreRatio:             0.8,
		RequireAllPassed:          true,
		MaxRegression:             0.1,
		RequireComparableBaseline: true,
		MetricMinRatios: map[string]float64{
			"verification_passed": 0.5,
		},
		FailOnMissingMetric: true,
	})
	if gate.Passed {
		t.Fatalf("gate passed, want failures")
	}
	wantSubstrings := []string{"scenario", "score", "regressed", "verification_passed"}
	for _, want := range wantSubstrings {
		if !gateFailureContains(gate, want) {
			t.Fatalf("gate failures = %v, want substring %q", gate.Failures, want)
		}
	}
}

func TestEvaluateGate_CohortMismatchOnlyBlocksRegressionGate(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{ScenarioID: "old", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1}}})
	current := BuildReport("current.jsonl", []Result{{ScenarioID: "new", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1}}})
	cmp := CompareReports(baseline, current)

	if gate := EvaluateGate(current, &cmp, GateOptions{RequireAllPassed: true}); !gate.Passed {
		t.Fatalf("unrelated require-pass gate failed on optional baseline mismatch: %+v", gate)
	}
	gate := EvaluateGate(current, &cmp, GateOptions{RequireComparableBaseline: true, MaxRegression: 0.1})
	if gate.Passed || !gateFailureContains(gate, "cohort mismatch") {
		t.Fatalf("regression gate = %+v, want fail-closed cohort mismatch", gate)
	}
}

func TestEvaluateGate_ZeroRegressionToleranceIsEnforced(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{ScenarioID: "a", Status: "passed", Score: ScoreSummary{Passed: 2, Total: 2}}})
	current := BuildReport("current.jsonl", []Result{{ScenarioID: "a", Status: "failed", Score: ScoreSummary{Passed: 1, Total: 2}}})
	cmp := CompareReports(baseline, current)
	gate := EvaluateGate(current, &cmp, GateOptions{RequireComparableBaseline: true, MaxRegression: 0})
	if gate.Passed || !gateFailureContains(gate, "regressed") {
		t.Fatalf("zero-tolerance gate = %+v, want regression failure", gate)
	}
}

func TestEvaluateGate_EngineComparisonPassesWithCompleteStableEvidence(t *testing.T) {
	zeroScoreRegression, zeroQualityRegressions := 0.0, 0
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools",
		Mode:         "auto",
		Provenance:   verifiedEnginePairProvenance(2),
		All: EngineCohortComparison{
			Pairs:  2,
			Hybrid: PairedHybridFunnel{TrustedRuntime: 2, PolicyObserved: 2, ModeMatched: 2, ExposureMatched: 2},
			Efficiency: PairedEfficiencyComparison{
				TotalTokens: PairedMetricComparison{Pairs: 2},
			},
		},
		Candidates: EngineCohortComparison{Pairs: 1},
		Controls:   EngineCohortComparison{Pairs: 1},
	}}}
	gate := EvaluateGate(report, nil, GateOptions{
		EngineModes:                 []string{"auto"},
		RequireCompleteEnginePairs:  true,
		MaxEngineScoreRegression:    &zeroScoreRegression,
		MaxEngineQualityRegressions: &zeroQualityRegressions,
	})
	if !gate.Passed {
		t.Fatalf("gate = %+v, want complete stable engine evidence to pass", gate)
	}
	report.EngineComparisons[0].All.Hybrid.TrustedRuntime = 1
	gate = EvaluateGate(report, nil, GateOptions{
		EngineModes:                []string{"auto"},
		RequireCompleteEnginePairs: true,
	})
	if gate.Passed || !gateFailureContains(gate, "trusted runtime journal evidence for 1/2") {
		t.Fatalf("gate = %+v, want untrusted paired evidence rejected explicitly", gate)
	}
}

func TestEvaluateGate_CompleteEnginePairsRejectsCrossCancellingExposureErrors(t *testing.T) {
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(2),
		All: EngineCohortComparison{
			Pairs: 2,
			Hybrid: PairedHybridFunnel{
				TrustedRuntime: 2, PolicyObserved: 2, ModeMatched: 2, Eligible: 1, Exposed: 1,
				ExposureGaps: 1, UnexpectedExposure: 1,
			},
			Efficiency: PairedEfficiencyComparison{TotalTokens: PairedMetricComparison{Pairs: 2}},
		},
	}}}
	gate := EvaluateGate(report, nil, GateOptions{RequireCompleteEnginePairs: true})
	if gate.Passed || !gateFailureContains(gate, "eligibility/exposure matched") ||
		!gateFailureContains(gate, "gaps=1 unexpected=1") {
		t.Fatalf("cross-cancelling complete gate = %+v", gate)
	}
}

func TestEvaluateGate_CompleteEnginePairsRejectsWrongPolicyMode(t *testing.T) {
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(1),
		All: EngineCohortComparison{
			Pairs: 1,
			Hybrid: PairedHybridFunnel{
				TrustedRuntime: 1, PolicyObserved: 1, ModeMismatches: 1, ExposureMatched: 1,
			},
			Efficiency: PairedEfficiencyComparison{TotalTokens: PairedMetricComparison{Pairs: 1}},
		},
	}}}
	gate := EvaluateGate(report, nil, GateOptions{RequireCompleteEnginePairs: true})
	if gate.Passed || !gateFailureContains(gate, "mode provenance matched") ||
		!gateFailureContains(gate, "mismatches=1") {
		t.Fatalf("wrong-mode complete gate = %+v", gate)
	}
}

func TestEvaluateGate_EngineComparisonFailsClosed(t *testing.T) {
	maxScoreRegression, maxQualityRegressions := 0.1, 0
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools",
		Mode:         "auto",
		Provenance:   verifiedEnginePairProvenance(2),
		All: EngineCohortComparison{
			Pairs:              2,
			ScoreDelta:         -0.25,
			QualityRegressions: []ScenarioIdentity{{ID: "aggregate", Variant: "p/m/trial=2/3"}},
			Efficiency: PairedEfficiencyComparison{
				TotalTokens: PairedMetricComparison{Pairs: 1},
			},
		},
		Candidates: EngineCohortComparison{Pairs: 1, ScoreDelta: -0.5},
		Controls:   EngineCohortComparison{Pairs: 1},
		Excluded:   EnginePairExclusions{CurrentOnly: 1},
	}}}
	gate := EvaluateGate(report, nil, GateOptions{
		EngineModes:                 []string{"auto"},
		RequireCompleteEnginePairs:  true,
		MaxEngineScoreRegression:    &maxScoreRegression,
		MaxEngineQualityRegressions: &maxQualityRegressions,
	})
	if gate.Passed {
		t.Fatalf("gate passed, want incomplete/regressed engine evidence rejected: %+v", gate)
	}
	for _, want := range []string{"pairing excluded", "headless metrics", "hybrid policy evidence", "all score regressed", "candidates score regressed", "quality regression"} {
		if !gateFailureContains(gate, want) {
			t.Fatalf("failures = %v, want %q", gate.Failures, want)
		}
	}
}

func TestEvaluateGate_EngineModeMustExist(t *testing.T) {
	zero := 0.0
	gate := EvaluateGate(Report{}, nil, GateOptions{EngineModes: []string{"auto"}, MaxEngineScoreRegression: &zero})
	if gate.Passed || !gateFailureContains(gate, "no paired comparison") {
		t.Fatalf("gate = %+v, want missing selected mode to fail closed", gate)
	}
	emptyComparison := Report{EngineComparisons: []EngineComparison{{BaselineMode: "tools", Mode: "auto"}}}
	gate = EvaluateGate(emptyComparison, nil, GateOptions{EngineModes: []string{"auto"}, MaxEngineScoreRegression: &zero})
	if gate.Passed || !gateFailureContains(gate, "no valid tools/auto pairs") {
		t.Fatalf("gate = %+v, want zero-pair comparison to fail closed", gate)
	}
}

func TestEvaluateGate_EngineEfficiencyThresholds(t *testing.T) {
	relative := -0.2
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(3),
		All: EngineCohortComparison{Pairs: 3},
		Candidates: EngineCohortComparison{Pairs: 3, Efficiency: PairedEfficiencyComparison{
			TotalTokens: PairedMetricComparison{
				Pairs: 3, RelativeDelta: &relative, Lower: 2, Equal: 1,
				EvidenceUnits: 3, UnitLower: 2, UnitEqual: 1,
			},
		}},
	}}}
	passing := EvaluateGate(report, nil, GateOptions{
		EngineMaxRelativeDeltas: map[string]float64{"candidates.total_tokens": -0.1},
		EngineMinLowerRatios:    map[string]float64{"candidates.total_tokens": 2.0 / 3.0},
	})
	if !passing.Passed {
		t.Fatalf("gate = %+v, want -20%% and 2/3 lower to satisfy thresholds", passing)
	}
	failing := EvaluateGate(report, nil, GateOptions{
		EngineMaxRelativeDeltas: map[string]float64{"candidates.total_tokens": -0.25},
		EngineMinLowerRatios:    map[string]float64{"candidates.total_tokens": 1},
	})
	if failing.Passed || !gateFailureContains(failing, "relative delta") || !gateFailureContains(failing, "clustered lower ratio") {
		t.Fatalf("gate = %+v, want both efficiency thresholds to fail", failing)
	}
}

func TestEvaluateGate_EngineLowerRatioClustersRepeatedTrials(t *testing.T) {
	var acc pairedMetricAccumulator
	for range 3 {
		acc.add("lower-unit", 100, 90)
	}
	for _, unit := range []string{"higher-unit-a", "higher-unit-b"} {
		acc.add(unit, 100, 90)
		acc.add(unit, 100, 110)
		acc.add(unit, 100, 110)
	}
	metric := acc.finalize()
	if metric.Lower != 5 || metric.Pairs != 9 || metric.UnitLower != 1 || metric.EvidenceUnits != 3 {
		t.Fatalf("direction evidence = %+v", metric)
	}
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(9), All: EngineCohortComparison{Pairs: 9},
		Candidates: EngineCohortComparison{Pairs: 9, Efficiency: PairedEfficiencyComparison{TotalTokens: metric}},
	}}}
	gate := EvaluateGate(report, nil, GateOptions{
		EngineMinLowerRatios: map[string]float64{"candidates.total_tokens": 0.5},
	})
	if gate.Passed || !gateFailureContains(gate, "clustered lower ratio 33.3%") {
		t.Fatalf("gate = %+v, want 1/3 clustered units to fail despite 5/9 lower trials", gate)
	}

	report.EngineComparisons[0].Candidates.Efficiency.TotalTokens.UnitHigher = 1
	malformed := EvaluateGate(report, nil, GateOptions{
		EngineMinLowerRatios: map[string]float64{"candidates.total_tokens": 0},
	})
	if malformed.Passed || !gateFailureContains(malformed, "incomplete clustered direction evidence") {
		t.Fatalf("malformed direction gate = %+v, want fail closed", malformed)
	}
	malformedSign := EvaluateGate(report, nil, GateOptions{
		EngineMaxLowerPValues: map[string]float64{"candidates.total_tokens": 1},
	})
	if malformedSign.Passed || !gateFailureContains(malformedSign, "incomplete clustered direction evidence") {
		t.Fatalf("malformed sign-test gate = %+v, want same direction provenance rejected", malformedSign)
	}
}

func TestEvaluateGate_EngineMedianRelativeDeltaThreshold(t *testing.T) {
	aggregate, median := -0.30, -0.05
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(3), All: EngineCohortComparison{Pairs: 3},
		Candidates: EngineCohortComparison{Pairs: 3, Efficiency: PairedEfficiencyComparison{
			TotalTokens: PairedMetricComparison{
				Pairs: 3, RelativeDelta: &aggregate, RelativePairs: 3, MedianRelativeDelta: &median,
				EvidenceUnits: 3, ClusteredRelativeEvidenceUnits: 3, ClusteredMedianRelativeDelta: &median,
			},
		}},
	}}}
	passing := EvaluateGate(report, nil, GateOptions{
		EngineMaxRelativeDeltas:       map[string]float64{"candidates.total_tokens": -0.20},
		EngineMaxMedianRelativeDeltas: map[string]float64{"candidates.total_tokens": -0.04},
	})
	if !passing.Passed {
		t.Fatalf("gate = %+v, want aggregate and robust magnitude thresholds to pass", passing)
	}
	failing := EvaluateGate(report, nil, GateOptions{
		EngineMaxRelativeDeltas:       map[string]float64{"candidates.total_tokens": -0.20},
		EngineMaxMedianRelativeDeltas: map[string]float64{"candidates.total_tokens": -0.10},
	})
	if failing.Passed || gateFailureContains(failing, "relative delta -30.0%") ||
		!gateFailureContains(failing, "clustered median relative delta -5.0%") {
		t.Fatalf("gate = %+v, want only the outlier-resistant magnitude gate to fail", failing)
	}
	report.EngineComparisons[0].Candidates.Efficiency.TotalTokens.ClusteredRelativeEvidenceUnits = 2
	incomplete := EvaluateGate(report, nil, GateOptions{
		EngineMaxMedianRelativeDeltas: map[string]float64{"candidates.total_tokens": 0},
	})
	if incomplete.Passed || !gateFailureContains(incomplete, "relative baselines for 2/3 clustered evidence") {
		t.Fatalf("gate = %+v, want zero-baseline subset to fail closed", incomplete)
	}
	invalid := EvaluateGate(report, nil, GateOptions{
		EngineMaxMedianRelativeDeltas: map[string]float64{"candidates.total_tokens": -1.01},
	})
	if invalid.Passed || !gateFailureContains(invalid, "invalid maximum median relative delta") {
		t.Fatalf("gate = %+v, want impossible savings threshold rejected", invalid)
	}
}

func TestEvaluateGate_EngineLowerSignTestThreshold(t *testing.T) {
	p := 1.0 / 64
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(6), All: EngineCohortComparison{Pairs: 6},
		Candidates: EngineCohortComparison{Pairs: 6, Efficiency: PairedEfficiencyComparison{
			TotalTokens: PairedMetricComparison{
				Pairs: 6, EvidenceUnits: 6, UnitLower: 6, LowerSignTestPValue: &p,
			},
		}},
	}}}
	passing := EvaluateGate(report, nil, GateOptions{
		EngineMaxLowerPValues: map[string]float64{"candidates.total_tokens": 0.05},
	})
	if !passing.Passed {
		t.Fatalf("gate = %+v, want six independent lower units to pass p<=.05", passing)
	}
	failing := EvaluateGate(report, nil, GateOptions{
		EngineMaxLowerPValues: map[string]float64{"candidates.total_tokens": 0.01},
	})
	if failing.Passed || !gateFailureContains(failing, "sign-test p-value") {
		t.Fatalf("gate = %+v, want stricter p threshold to fail", failing)
	}
	report.EngineComparisons[0].Candidates.Efficiency.TotalTokens.LowerSignTestPValue = nil
	missing := EvaluateGate(report, nil, GateOptions{
		EngineMaxLowerPValues: map[string]float64{"candidates.total_tokens": 0.05},
	})
	if missing.Passed || !gateFailureContains(missing, "no non-tied evidence units") {
		t.Fatalf("gate = %+v, want missing sign-test evidence to fail closed", missing)
	}
	report.EngineComparisons[0].Candidates.Efficiency.TotalTokens.LowerSignTestPValue = &p
	report.EngineComparisons[0].Excluded.CurrentOnly = 1
	excluded := EvaluateGate(report, nil, GateOptions{
		EngineMaxLowerPValues: map[string]float64{"candidates.total_tokens": 0.05},
	})
	if excluded.Passed || !gateFailureContains(excluded, "cannot gate incomplete evidence") {
		t.Fatalf("gate = %+v, want excluded cohort to invalidate inference", excluded)
	}
	report.EngineComparisons[0].Excluded = EnginePairExclusions{}
	report.EngineComparisons[0].Candidates.Efficiency.TotalTokens.Pairs = 5
	unmeasured := EvaluateGate(report, nil, GateOptions{
		EngineMaxLowerPValues: map[string]float64{"candidates.total_tokens": 0.05},
	})
	if unmeasured.Passed || !gateFailureContains(unmeasured, "measurements for 5/6") {
		t.Fatalf("gate = %+v, want partial metric measurements to invalidate inference", unmeasured)
	}
}

func TestEvaluateGate_EngineReplUseRatios(t *testing.T) {
	report := Report{EngineComparisons: []EngineComparison{{
		BaselineMode: "tools", Mode: "auto", Provenance: verifiedEnginePairProvenance(5),
		All:        EngineCohortComparison{Pairs: 5, Hybrid: PairedHybridFunnel{TrustedRuntime: 5, PolicyObserved: 5, ModeMatched: 5, ExposureMatched: 5, Used: 2}},
		Candidates: EngineCohortComparison{Pairs: 3, Hybrid: PairedHybridFunnel{TrustedRuntime: 3, PolicyObserved: 3, ModeMatched: 3, ExposureMatched: 3, Used: 2, UseRatio: 2.0 / 3.0}},
		Controls:   EngineCohortComparison{Pairs: 2, Hybrid: PairedHybridFunnel{TrustedRuntime: 2, PolicyObserved: 2, ModeMatched: 2, ExposureMatched: 2}},
	}}}
	passing := EvaluateGate(report, nil, GateOptions{
		EngineMinReplUseRatios: map[string]float64{"candidates": 2.0 / 3.0},
		EngineMaxReplUseRatios: map[string]float64{"controls": 0},
	})
	if !passing.Passed {
		t.Fatalf("gate = %+v, want candidate adoption/control restraint to pass", passing)
	}
	failingMin := EvaluateGate(report, nil, GateOptions{
		EngineMinReplUseRatios: map[string]float64{"candidates": 1},
	})
	if failingMin.Passed || !gateFailureContains(failingMin, "below required") {
		t.Fatalf("minimum adoption gate = %+v", failingMin)
	}
	report.EngineComparisons[0].Controls.Hybrid.Used = 1
	failingMax := EvaluateGate(report, nil, GateOptions{
		EngineMaxReplUseRatios: map[string]float64{"controls": 0},
	})
	if failingMax.Passed || !gateFailureContains(failingMax, "exceeds maximum") {
		t.Fatalf("maximum control adoption gate = %+v", failingMax)
	}
	empty := EvaluateGate(report, nil, GateOptions{
		EngineMinReplUseRatios: map[string]float64{"unknown": 0.5},
	})
	if empty.Passed || !gateFailureContains(empty, "invalid cohort") {
		t.Fatalf("invalid cohort gate = %+v", empty)
	}
	missingEvidenceReport := report
	missingEvidenceReport.EngineComparisons = append([]EngineComparison(nil), report.EngineComparisons...)
	missingEvidenceReport.EngineComparisons[0].Candidates.Hybrid.PolicyObserved = 2
	missingEvidence := EvaluateGate(missingEvidenceReport, nil, GateOptions{
		EngineMinReplUseRatios: map[string]float64{"candidates": 0.5},
	})
	if missingEvidence.Passed || !gateFailureContains(missingEvidence, "policy evidence") {
		t.Fatalf("missing adoption evidence gate = %+v", missingEvidence)
	}
	exposureMismatchReport := report
	exposureMismatchReport.EngineComparisons = append([]EngineComparison(nil), report.EngineComparisons...)
	exposureMismatchReport.EngineComparisons[0].Candidates.Hybrid.ExposureMatched = 2
	exposureMismatchReport.EngineComparisons[0].Candidates.Hybrid.ExposureGaps = 1
	exposureMismatch := EvaluateGate(exposureMismatchReport, nil, GateOptions{
		EngineMinReplUseRatios: map[string]float64{"candidates": 0.5},
	})
	if exposureMismatch.Passed || !gateFailureContains(exposureMismatch, "eligibility/exposure matched") {
		t.Fatalf("mismatched exposure adoption gate = %+v", exposureMismatch)
	}
	modeMismatchReport := report
	modeMismatchReport.EngineComparisons = append([]EngineComparison(nil), report.EngineComparisons...)
	modeMismatchReport.EngineComparisons[0].Candidates.Hybrid.ModeMatched = 2
	modeMismatchReport.EngineComparisons[0].Candidates.Hybrid.ModeMismatches = 1
	modeMismatch := EvaluateGate(modeMismatchReport, nil, GateOptions{
		EngineMinReplUseRatios: map[string]float64{"candidates": 0.5},
	})
	if modeMismatch.Passed || !gateFailureContains(modeMismatch, "mode provenance matched") {
		t.Fatalf("wrong-mode adoption gate = %+v", modeMismatch)
	}
	report.EngineComparisons[0].Controls = EngineCohortComparison{}
	noPairs := EvaluateGate(report, nil, GateOptions{
		EngineMaxReplUseRatios: map[string]float64{"controls": 0},
	})
	if noPairs.Passed || !gateFailureContains(noPairs, "no paired REPL adoption evidence") {
		t.Fatalf("zero-pair adoption gate = %+v", noPairs)
	}
}

func verifiedEnginePairProvenance(pairs int) EnginePairProvenance {
	return EnginePairProvenance{
		Pairs: pairs, ScenarioSpecVerified: pairs, RunSpecVerified: pairs, ClassificationVerified: pairs,
	}
}

func TestCompareReports_DryRunEvidenceIsIncomparable(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{ScenarioID: "a", Status: "dry_run"}})
	current := BuildReport("current.jsonl", []Result{{ScenarioID: "a", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1}}})
	cmp := CompareReports(baseline, current)
	if cmp.InvalidEvidence == nil || cmp.InvalidEvidence.BaselineDryRun != 1 {
		t.Fatalf("comparison = %+v, want invalid dry-run evidence", cmp)
	}
	if cmp.ScoreDelta != 0 || len(cmp.Metrics) != 0 || len(cmp.Scenarios) != 0 {
		t.Fatalf("dry-run comparison exposed misleading deltas: %+v", cmp)
	}
	gate := EvaluateGate(current, &cmp, GateOptions{RequireComparableBaseline: true, MaxRegression: 0.1})
	if gate.Passed || !gateFailureContains(gate, "dry-run evidence") {
		t.Fatalf("gate = %+v, want invalid-evidence failure", gate)
	}
}

func TestCompareReports_DuplicateScenarioIdentityIsCohortMismatch(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{
		{ScenarioID: "a", Provider: "glm", Model: "glm-5.2", Status: "passed"},
		{ScenarioID: "a", Provider: "glm", Model: "glm-5.2", Status: "passed"},
	})
	current := BuildReport("current.jsonl", []Result{{ScenarioID: "a", Provider: "glm", Model: "glm-5.2", Status: "passed"}})
	cmp := CompareReports(baseline, current)
	if cmp.CohortMismatch == nil || len(cmp.CohortMismatch.BaselineDuplicates) != 1 {
		t.Fatalf("comparison = %+v, want duplicate baseline identity", cmp)
	}
	if len(cmp.Scenarios) != 0 {
		t.Fatalf("duplicate identity exposed ambiguous scenario deltas: %+v", cmp.Scenarios)
	}
}

func TestCompareReports_EmptyAndNotExecutedEvidenceIsInvalid(t *testing.T) {
	t.Run("empty cohorts", func(t *testing.T) {
		cmp := CompareReports(BuildReport("baseline.jsonl", nil), BuildReport("current.jsonl", nil))
		if cmp.InvalidEvidence == nil || !cmp.InvalidEvidence.BaselineEmpty || !cmp.InvalidEvidence.CurrentEmpty {
			t.Fatalf("comparison = %+v, want both empty cohorts invalid", cmp)
		}
		gate := EvaluateGate(Report{}, &cmp, GateOptions{RequireComparableBaseline: true, MaxRegression: 0.1})
		if gate.Passed || !gateFailureContains(gate, "invalid evidence") {
			t.Fatalf("gate = %+v, want empty comparison rejected", gate)
		}
	})

	t.Run("setup failure", func(t *testing.T) {
		baseline := BuildReport("baseline.jsonl", []Result{{ScenarioID: "a", Status: "setup_failed"}})
		current := BuildReport("current.jsonl", []Result{{ScenarioID: "a", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1}}})
		cmp := CompareReports(baseline, current)
		if cmp.InvalidEvidence == nil || cmp.InvalidEvidence.BaselineNotExecuted != 1 {
			t.Fatalf("comparison = %+v, want setup failure marked not executed", cmp)
		}
		gate := EvaluateGate(current, &cmp, GateOptions{RequireComparableBaseline: true, MaxRegression: 0.1})
		if gate.Passed || !gateFailureContains(gate, "not-executed") {
			t.Fatalf("gate = %+v, want setup-failed baseline rejected", gate)
		}
	})
}

func TestCompareReports_ScenarioSpecFingerprint(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{
		ScenarioID: "a", ScenarioSpecHash: "spec-v1", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1},
	}})
	current := BuildReport("current.jsonl", []Result{{
		ScenarioID: "a", ScenarioSpecHash: "spec-v2", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1},
	}})
	cmp := CompareReports(baseline, current)
	if cmp.CohortMismatch == nil || len(cmp.CohortMismatch.SpecMismatches) != 1 {
		t.Fatalf("comparison = %+v, want changed scenario spec mismatch", cmp)
	}
	if cmp.ScoreDelta != 0 || len(cmp.Metrics) != 0 || len(cmp.Scenarios) != 0 {
		t.Fatalf("changed scenario spec exposed incomparable deltas: %+v", cmp)
	}

	// Additive compatibility: committed baselines written before the optional
	// hash existed remain comparable until they are refreshed.
	legacy := BuildReport("legacy.jsonl", []Result{{ScenarioID: "a", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1}}})
	if legacyCmp := CompareReports(legacy, current); legacyCmp.CohortMismatch != nil || legacyCmp.InvalidEvidence != nil {
		t.Fatalf("legacy comparison = %+v, want missing optional hash accepted", legacyCmp)
	}
}

// A require-all-passed gate over zero scenarios must FAIL, not pass vacuously —
// zero scenarios almost always means a misconfigured filter and a green CI on no
// work is a false signal.
func TestEvaluateGate_RequireAllPassedFailsOnZeroScenarios(t *testing.T) {
	report := BuildReport("empty.jsonl", nil)
	gate := EvaluateGate(report, nil, GateOptions{RequireAllPassed: true})
	if gate.Passed {
		t.Fatal("gate passed on zero scenarios, want failure")
	}
	if !gateFailureContains(gate, "no scenarios") {
		t.Fatalf("gate failures = %v, want a 'no scenarios' failure", gate.Failures)
	}
}

// A metric present in only one report (e.g. a --scenario-scoped subset run vs a
// full baseline) must NOT be emitted as a comparison delta — otherwise the
// absent side defaults to 0 and it looks like a spurious ±100% regression.
func TestCompareReports_OneSidedMetricNotComparable(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{
		ScenarioID: "a", Status: "passed",
		Metrics: map[string]bool{"verification_passed": true, "only_in_baseline": true},
		Score:   ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
	}})
	current := BuildReport("current.jsonl", []Result{{
		ScenarioID: "a", Status: "passed",
		Metrics: map[string]bool{"verification_passed": true},
		Score:   ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
	}})

	cmp := CompareReports(baseline, current)
	for _, d := range cmp.Metrics {
		if d.Name == "only_in_baseline" {
			t.Fatalf("one-sided metric %q must be excluded from the comparison (got delta %+v)", d.Name, d)
		}
	}
	// The shared metric is still compared.
	if metricDeltaByName(cmp.Metrics, "verification_passed").Name == "" {
		t.Fatal("shared metric should still be compared")
	}

	// And it must not surface as a regression in diagnose.
	diagnosis := DiagnoseReport(current, &cmp)
	for _, r := range diagnosis.Regressions {
		if r.Name == "only_in_baseline" {
			t.Fatalf("one-sided metric must not be flagged as a regression: %+v", r)
		}
	}
}

func TestParseMetricThresholds_AcceptsRatiosAndPercents(t *testing.T) {
	got, err := ParseMetricThresholds([]string{"verification_passed=90%", "task_completed=0.8"})
	if err != nil {
		t.Fatalf("ParseMetricThresholds() error = %v", err)
	}
	if got["verification_passed"] != 0.9 || got["task_completed"] != 0.8 {
		t.Fatalf("thresholds = %#v, want parsed ratios", got)
	}
}

func TestParseMetricThresholds_RejectsInvalidShape(t *testing.T) {
	if _, err := ParseMetricThresholds([]string{"verification_passed"}); err == nil {
		t.Fatal("ParseMetricThresholds() error = nil, want missing '=' error")
	}
	if _, err := ParseMetricThresholds([]string{"verification_passed=120%"}); err == nil {
		t.Fatal("ParseMetricThresholds() error = nil, want out-of-range error")
	}
}

func TestParseEngineEfficiencyThresholds(t *testing.T) {
	maxDeltas, err := ParseEngineMaxRelativeDeltas([]string{
		"candidates.total_tokens=-5%", "controls.duration_ms=0.1", "all.input_tokens=0%",
		"all.uncached_input_tokens=0%", "all.cache_read_input_tokens=0%", "all.output_tokens=0%",
	})
	if err != nil {
		t.Fatal(err)
	}
	medianDeltas, err := ParseEngineMaxMedianRelativeDeltas([]string{"candidates.total_tokens=-5%"})
	if err != nil || medianDeltas["candidates.total_tokens"] != -0.05 {
		t.Fatalf("median relative deltas = %+v, err=%v", medianDeltas, err)
	}
	if maxDeltas["candidates.total_tokens"] != -0.05 || maxDeltas["controls.duration_ms"] != 0.1 {
		t.Fatalf("max deltas = %+v", maxDeltas)
	}
	for _, key := range []string{"all.input_tokens", "all.uncached_input_tokens", "all.cache_read_input_tokens", "all.output_tokens"} {
		if maxDeltas[key] != 0 {
			t.Fatalf("component threshold %q = %v, want 0", key, maxDeltas[key])
		}
	}
	lower, err := ParseEngineMinLowerRatios([]string{"candidates.model_rounds=67%"})
	if err != nil || lower["candidates.model_rounds"] != 0.67 {
		t.Fatalf("lower ratios = %+v, err=%v", lower, err)
	}
	pValues, err := ParseEngineMaxLowerPValues([]string{"candidates.total_tokens=5%"})
	if err != nil || pValues["candidates.total_tokens"] != 0.05 {
		t.Fatalf("sign-test thresholds = %+v, err=%v", pValues, err)
	}
	for _, invalid := range []string{
		"candidates.unknown=5%", "unknown.total_tokens=5%", "candidates.total_tokens", "candidates.total_tokens=NaN",
	} {
		if _, err := ParseEngineMaxRelativeDeltas([]string{invalid}); err == nil {
			t.Fatalf("invalid threshold %q was accepted", invalid)
		}
	}
	if _, err := ParseEngineMinLowerRatios([]string{"candidates.total_tokens=101%"}); err == nil {
		t.Fatal("lower ratio above 100% was accepted")
	}
	if _, err := ParseEngineMaxLowerPValues([]string{"candidates.total_tokens=101%"}); err == nil {
		t.Fatal("sign-test p-value above 100% was accepted")
	}
}

func TestParseEngineReplUseRatios(t *testing.T) {
	ratios, err := ParseEngineReplUseRatios([]string{"candidates=67%", "controls=0"})
	if err != nil || ratios["candidates"] != 0.67 || ratios["controls"] != 0 {
		t.Fatalf("ratios = %+v err=%v", ratios, err)
	}
	for _, invalid := range []string{"candidate=50%", "candidates", "controls=101%", "all=NaN"} {
		if _, err := ParseEngineReplUseRatios([]string{invalid}); err == nil {
			t.Fatalf("invalid REPL use threshold %q was accepted", invalid)
		}
	}
}

func TestParseRatio_RejectsNonFiniteValues(t *testing.T) {
	for _, value := range []string{"NaN", "NaN%", "+Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseRatio(value); err == nil {
				t.Fatalf("ParseRatio(%q) error = nil, want non-finite rejection", value)
			}
			if _, err := ParseMetricThresholds([]string{"metric=" + value}); err == nil {
				t.Fatalf("ParseMetricThresholds(%q) error = nil, want non-finite rejection", value)
			}
		})
	}
}

func metricByName(metrics []MetricSummary, name string) MetricSummary {
	for _, metric := range metrics {
		if metric.Name == name {
			return metric
		}
	}
	return MetricSummary{}
}

func gateFailureContains(gate GateResult, want string) bool {
	for _, failure := range gate.Failures {
		if strings.Contains(failure, want) {
			return true
		}
	}
	return false
}

func metricDeltaByName(metrics []MetricDelta, name string) MetricDelta {
	for _, metric := range metrics {
		if metric.Name == name {
			return metric
		}
	}
	return MetricDelta{}
}
