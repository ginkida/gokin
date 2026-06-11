package evals

import (
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
	baseline := BuildReport("baseline.jsonl", []Result{{
		ScenarioID: "a",
		Status:     "passed",
		Metrics: map[string]bool{
			"task_completed":      true,
			"verification_passed": true,
		},
		Score: ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
	}})
	cmp := CompareReports(baseline, report)

	gate := EvaluateGate(report, &cmp, GateOptions{
		MinScoreRatio:    0.8,
		RequireAllPassed: true,
		MaxRegression:    0.1,
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
