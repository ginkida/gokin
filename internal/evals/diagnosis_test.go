package evals

import (
	"strings"
	"testing"
)

func TestDiagnoseReport_RecommendsPromptAndToolActions(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{
		{
			ScenarioID: "fix-test",
			Status:     "failed",
			Metrics: map[string]bool{
				"task_completed":                     true,
				"verification_passed":                false,
				"final_answer_mentions_verification": false,
				"files_read_recorded":                false,
			},
			Score: ScoreSummary{Passed: 1, Total: 4, Ratio: 0.25},
		},
	})

	diagnosis := DiagnoseReport(report, nil)
	if len(diagnosis.WeakMetrics) != 3 {
		t.Fatalf("WeakMetrics = %d, want 3", len(diagnosis.WeakMetrics))
	}
	if len(diagnosis.FailedScenarios) != 1 {
		t.Fatalf("FailedScenarios = %d, want 1", len(diagnosis.FailedScenarios))
	}
	if !hasRecommendationArea(diagnosis.Recommendations, "eval-target") {
		t.Fatalf("recommendations = %+v, want eval-target action", diagnosis.Recommendations)
	}
	if !hasRecommendationArea(diagnosis.Recommendations, "prompt") {
		t.Fatalf("recommendations = %+v, want prompt action", diagnosis.Recommendations)
	}
	if !hasRecommendationArea(diagnosis.Recommendations, "tool-output") {
		t.Fatalf("recommendations = %+v, want tool-output action", diagnosis.Recommendations)
	}
}

func TestDiagnoseReport_IncludesRegressions(t *testing.T) {
	baseline := BuildReport("baseline.jsonl", []Result{{
		ScenarioID: "a",
		Status:     "passed",
		Metrics: map[string]bool{
			"verification_passed": true,
			"journal_present":     true,
		},
		Score: ScoreSummary{Passed: 2, Total: 2, Ratio: 1},
	}})
	current := BuildReport("current.jsonl", []Result{{
		ScenarioID: "a",
		Status:     "passed",
		Metrics: map[string]bool{
			"verification_passed": false,
			"journal_present":     false,
		},
		Score: ScoreSummary{Passed: 0, Total: 2, Ratio: 0},
	}})
	cmp := CompareReports(baseline, current)

	diagnosis := DiagnoseReport(current, &cmp)
	if len(diagnosis.Regressions) != 2 {
		t.Fatalf("Regressions = %d, want 2", len(diagnosis.Regressions))
	}
	if !hasRecommendationArea(diagnosis.Recommendations, "prompt-regression") {
		t.Fatalf("recommendations = %+v, want prompt-regression action", diagnosis.Recommendations)
	}
	if !hasRecommendationArea(diagnosis.Recommendations, "tool-output-regression") {
		t.Fatalf("recommendations = %+v, want tool-output-regression action", diagnosis.Recommendations)
	}
}

func TestDiagnoseReport_AllPassingRecommendsBaseline(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{{
		ScenarioID: "a",
		Status:     "passed",
		Metrics: map[string]bool{
			"verification_passed": true,
		},
		Score: ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
	}})

	diagnosis := DiagnoseReport(report, nil)
	if len(diagnosis.Recommendations) != 1 {
		t.Fatalf("Recommendations = %d, want 1", len(diagnosis.Recommendations))
	}
	if diagnosis.Recommendations[0].Area != "repeat-loop" {
		t.Fatalf("recommendation = %+v, want repeat-loop", diagnosis.Recommendations[0])
	}
}

func TestDiagnoseReport_DetectsCrossCancellingHybridExposureErrors(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{
		{ScenarioID: "gap", EngineMode: "auto", Status: "passed", Journal: &JournalSummary{TrustedRuntime: true,
			HybridPolicy: &HybridPolicySummary{Mode: "auto", REPLEligible: true, REPLEnabled: false},
		}},
		{ScenarioID: "leak", EngineMode: "auto", Status: "passed", Journal: &JournalSummary{TrustedRuntime: true,
			HybridPolicy: &HybridPolicySummary{Mode: "auto", REPLEligible: false, REPLEnabled: true},
		}},
	})
	diagnosis := DiagnoseReport(report, nil)
	for _, rec := range diagnosis.Recommendations {
		if rec.Area == "hybrid-exposure" {
			if !strings.Contains(rec.Reason, "1 availability gap") || !strings.Contains(rec.Reason, "1 unexpected exposure") {
				t.Fatalf("recommendation = %+v", rec)
			}
			return
		}
	}
	t.Fatalf("recommendations = %+v, want hybrid-exposure", diagnosis.Recommendations)
}

func TestDiagnoseReport_DetectsHybridPolicyModeMismatch(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{{
		ScenarioID: "wrong-mode", EngineMode: "auto", Status: "passed",
		Journal: &JournalSummary{TrustedRuntime: true, HybridPolicy: &HybridPolicySummary{Mode: "tools"}},
	}})
	diagnosis := DiagnoseReport(report, nil)
	for _, rec := range diagnosis.Recommendations {
		if rec.Area == "hybrid-policy-provenance" {
			if !strings.Contains(rec.Reason, "1 hybrid policy event") || !strings.Contains(rec.Action, "GOKIN_ENGINE_MODE") {
				t.Fatalf("recommendation = %+v", rec)
			}
			return
		}
	}
	t.Fatalf("recommendations = %+v, want hybrid-policy-provenance", diagnosis.Recommendations)
}

func TestDiagnoseReport_ExplainsHybridEfficientPathFailure(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{{
		ScenarioID: "redundant-scan", EngineMode: "auto", Status: "failed",
		Metrics: map[string]bool{"hybrid_efficient_path": false},
		Score:   ScoreSummary{Total: 1},
	}})
	diagnosis := DiagnoseReport(report, nil)
	for _, rec := range diagnosis.Recommendations {
		if rec.Area == "hybrid-efficiency" {
			for _, want := range []string{"scan ops", "index refreshes", "repl_exec"} {
				if !strings.Contains(rec.Action, want) {
					t.Fatalf("recommendation action = %q, want %q", rec.Action, want)
				}
			}
			return
		}
	}
	t.Fatalf("recommendations = %+v, want hybrid-efficiency", diagnosis.Recommendations)
}

func TestDiagnoseReport_DryRunIsNotDiagnosedAsPassingEvidence(t *testing.T) {
	report := BuildReport("results.jsonl", []Result{{ScenarioID: "a", Status: "dry_run"}})
	diagnosis := DiagnoseReport(report, nil)
	if diagnosis.DryRun != 1 || !hasRecommendationArea(diagnosis.Recommendations, "eval-execution") {
		t.Fatalf("diagnosis = %+v, want explicit dry-run execution recommendation", diagnosis)
	}
	if hasRecommendationArea(diagnosis.Recommendations, "repeat-loop") {
		t.Fatalf("dry-run was incorrectly diagnosed as all passing: %+v", diagnosis.Recommendations)
	}
}

func TestDiagnoseReport_CohortRecommendationIncludesDuplicateAndSpecCounts(t *testing.T) {
	current := BuildReport("current.jsonl", []Result{{ScenarioID: "a", Status: "passed"}})
	cmp := Comparison{CohortMismatch: &CohortMismatch{
		BaselineDuplicates: []ScenarioIdentity{{ID: "a"}},
		CurrentDuplicates:  []ScenarioIdentity{{ID: "b"}},
		SpecMismatches:     []ScenarioIdentity{{ID: "c"}},
	}}
	diagnosis := DiagnoseReport(current, &cmp)
	for _, rec := range diagnosis.Recommendations {
		if rec.Area == "eval-cohort" {
			for _, want := range []string{"1 duplicate baseline", "1 duplicate current", "1 changed spec"} {
				if !strings.Contains(rec.Reason, want) {
					t.Fatalf("recommendation reason = %q, want %q", rec.Reason, want)
				}
			}
			return
		}
	}
	t.Fatalf("recommendations = %+v, want eval-cohort", diagnosis.Recommendations)
}

func hasRecommendationArea(recs []Recommendation, area string) bool {
	for _, rec := range recs {
		if rec.Area == area {
			return true
		}
	}
	return false
}
