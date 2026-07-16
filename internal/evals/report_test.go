package evals

import (
	"encoding/json"
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
