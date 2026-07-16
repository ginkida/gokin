package evals

import (
	"fmt"
	"sort"
	"strings"
)

// Diagnosis turns an eval report into prioritized next actions for the
// prompt/tool improvement loop.
type Diagnosis struct {
	ResultsPath     string                     `json:"results_path,omitempty"`
	Score           ScoreSummary               `json:"score"`
	DryRun          int                        `json:"dry_run"`
	CohortMismatch  *CohortMismatch            `json:"cohort_mismatch,omitempty"`
	InvalidEvidence *ComparisonInvalidEvidence `json:"invalid_evidence,omitempty"`
	WeakMetrics     []MetricSummary            `json:"weak_metrics,omitempty"`
	Regressions     []MetricDelta              `json:"regressions,omitempty"`
	FailedScenarios []ScenarioSummary          `json:"failed_scenarios,omitempty"`
	Recommendations []Recommendation           `json:"recommendations"`
}

// Recommendation is one actionable improvement candidate.
type Recommendation struct {
	Area     string `json:"area"`
	Priority int    `json:"priority"`
	Reason   string `json:"reason"`
	Action   string `json:"action"`
}

// DiagnoseReport identifies the weakest measured signals and maps them to
// concrete prompt/tool/harness actions.
func DiagnoseReport(report Report, comparison *Comparison) Diagnosis {
	diagnosis := Diagnosis{
		ResultsPath: report.ResultsPath,
		Score:       report.Score,
		DryRun:      report.DryRun,
	}
	if comparison != nil {
		diagnosis.CohortMismatch = comparison.CohortMismatch
		diagnosis.InvalidEvidence = comparison.InvalidEvidence
	}

	for _, metric := range report.Metrics {
		if metric.Total > 0 && metric.Ratio < 1 {
			diagnosis.WeakMetrics = append(diagnosis.WeakMetrics, metric)
		}
	}
	sort.Slice(diagnosis.WeakMetrics, func(i, j int) bool {
		if diagnosis.WeakMetrics[i].Ratio != diagnosis.WeakMetrics[j].Ratio {
			return diagnosis.WeakMetrics[i].Ratio < diagnosis.WeakMetrics[j].Ratio
		}
		return diagnosis.WeakMetrics[i].Name < diagnosis.WeakMetrics[j].Name
	})

	for _, scenario := range report.Scenarios {
		if scenario.Status != "passed" && scenario.Status != "dry_run" {
			diagnosis.FailedScenarios = append(diagnosis.FailedScenarios, scenario)
		}
	}

	if comparison != nil {
		for _, metric := range comparison.Metrics {
			if metric.Delta < 0 {
				diagnosis.Regressions = append(diagnosis.Regressions, metric)
			}
		}
		sort.Slice(diagnosis.Regressions, func(i, j int) bool {
			if diagnosis.Regressions[i].Delta != diagnosis.Regressions[j].Delta {
				return diagnosis.Regressions[i].Delta < diagnosis.Regressions[j].Delta
			}
			return diagnosis.Regressions[i].Name < diagnosis.Regressions[j].Name
		})
	}

	recommendations := map[string]Recommendation{}
	addRecommendation := func(rec Recommendation) {
		if existing, ok := recommendations[rec.Area]; ok && existing.Priority <= rec.Priority {
			return
		}
		recommendations[rec.Area] = rec
	}

	if report.DryRun > 0 {
		addRecommendation(Recommendation{
			Area:     "eval-execution",
			Priority: 5,
			Reason:   fmt.Sprintf("%d scenario(s) were only prepared in dry-run mode", report.DryRun),
			Action:   "Run the same cohort without --dry-run before treating its score or quality gate as evidence.",
		})
	}
	if diagnosis.CohortMismatch != nil {
		addRecommendation(Recommendation{
			Area:     "eval-cohort",
			Priority: 8,
			Reason: fmt.Sprintf("baseline and current cohorts differ (%d baseline-only, %d current-only, %d duplicate baseline, %d duplicate current, %d changed spec)",
				len(diagnosis.CohortMismatch.BaselineOnly), len(diagnosis.CohortMismatch.CurrentOnly),
				len(diagnosis.CohortMismatch.BaselineDuplicates), len(diagnosis.CohortMismatch.CurrentDuplicates),
				len(diagnosis.CohortMismatch.SpecMismatches)),
			Action: "Re-run or filter both result sets to the same scenario and provider/model variants before diagnosing regressions.",
		})
	}
	if diagnosis.InvalidEvidence != nil {
		addRecommendation(Recommendation{
			Area:     "eval-evidence",
			Priority: 7,
			Reason: fmt.Sprintf("comparison lacks executed evidence (empty baseline=%t/current=%t; dry-run=%d baseline/%d current; not-executed=%d baseline/%d current)",
				diagnosis.InvalidEvidence.BaselineEmpty, diagnosis.InvalidEvidence.CurrentEmpty,
				diagnosis.InvalidEvidence.BaselineDryRun, diagnosis.InvalidEvidence.CurrentDryRun,
				diagnosis.InvalidEvidence.BaselineNotExecuted, diagnosis.InvalidEvidence.CurrentNotExecuted),
			Action: "Execute both baseline and current cohorts fully before diagnosing score regressions.",
		})
	}

	if len(diagnosis.FailedScenarios) > 0 {
		addRecommendation(Recommendation{
			Area:     "eval-target",
			Priority: 10,
			Reason:   fmt.Sprintf("%d scenario(s) failed", len(diagnosis.FailedScenarios)),
			Action:   "Run the failing scenario(s) first with --keep-workspaces, inspect agent output and verification output, then make the smallest prompt/tool change that addresses the observed failure.",
		})
	}

	for _, metric := range diagnosis.WeakMetrics {
		rec := recommendationForMetric(metric)
		if rec.Area != "" {
			addRecommendation(rec)
		}
	}
	for _, regression := range diagnosis.Regressions {
		rec := recommendationForRegression(regression)
		if rec.Area != "" {
			addRecommendation(rec)
		}
	}

	for _, rec := range recommendations {
		diagnosis.Recommendations = append(diagnosis.Recommendations, rec)
	}
	sort.Slice(diagnosis.Recommendations, func(i, j int) bool {
		if diagnosis.Recommendations[i].Priority != diagnosis.Recommendations[j].Priority {
			return diagnosis.Recommendations[i].Priority < diagnosis.Recommendations[j].Priority
		}
		return diagnosis.Recommendations[i].Area < diagnosis.Recommendations[j].Area
	})

	if len(diagnosis.Recommendations) == 0 {
		diagnosis.Recommendations = append(diagnosis.Recommendations, Recommendation{
			Area:     "repeat-loop",
			Priority: 90,
			Reason:   "all measured metrics are currently passing",
			Action:   "Preserve this results file as a baseline before changing prompts, tools, routing, or model settings.",
		})
	}
	return diagnosis
}

func recommendationForMetric(metric MetricSummary) Recommendation {
	switch metric.Name {
	case "verification_passed":
		return metricRecommendation(metric, "prompt", 20,
			"verification did not pass consistently",
			"Strengthen the system prompt around root-cause investigation and narrow verification; make failing bash/test output point to the next focused command.")
	case "final_answer_mentions_verification":
		return metricRecommendation(metric, "prompt", 30,
			"final answers omit verification evidence",
			"Tighten final-answer instructions so every completed coding task reports the verification command and result, or explicitly says verification was not run.")
	case "files_read_recorded":
		return metricRecommendation(metric, "tool-output", 35,
			"agent is editing or answering without recorded file reads",
			"Improve read/grep guidance so search results push the model to open relevant files before editing; keep read-before-edit invariants visible.")
	case "files_edited_recorded":
		return metricRecommendation(metric, "tool-output", 35,
			"file edits are not consistently represented in journal evidence",
			"Improve write/edit tool results and journal capture so changed paths are explicit and easy for the model to cite.")
	case "verification_recorded":
		return metricRecommendation(metric, "tool-output", 35,
			"verification commands are not consistently represented in journal evidence",
			"Improve bash/run-test outputs and journal tagging so validation commands are captured as verification evidence.")
	case "no_false_file_claims":
		return metricRecommendation(metric, "prompt", 25,
			"agent is claiming files not actually changed",
			"Strengthen final-answer constraints: cite only files present in changed-files evidence; avoid speculative file lists.")
	case "tool_calls_reasonable":
		return metricRecommendation(metric, "prompt", 40,
			"tool-call budget is being exceeded",
			"Tighten exploration policy: search broadly once, read targeted files, avoid repeated equivalent grep/read calls, and stop once verification evidence is sufficient.")
	case "touched_files_scoped":
		return metricRecommendation(metric, "prompt", 25,
			"agent touched files outside expected scope",
			"Strengthen scope rules: modify only task-relevant source/tests and avoid generated, vendored, dependency, and unrelated files.")
	case "journal_present":
		return metricRecommendation(metric, "harness", 45,
			"execution journal is missing",
			"Fix headless/eval runtime wiring so .gokin/execution_journal.jsonl is written inside each eval workspace.")
	case "task_completed":
		return metricRecommendation(metric, "headless-runtime", 15,
			"agent command did not complete consistently",
			"Inspect provider/runtime errors, headless prompt handling, auth, workspace permissions, and empty-response retry behavior before tuning prompts.")
	case "answer_contains_required":
		return metricRecommendation(metric, "prompt", 20,
			"final answers omit the required conclusion (e.g. naming the caller in an investigation)",
			"Tighten final-answer instructions so investigation/trap tasks state the concrete finding the scenario requires, not a vague summary.")
	case "required_files_changed":
		return metricRecommendation(metric, "prompt", 20,
			"agent left a file unchanged that the task required modifying (likely a no-op on a green scenario)",
			"Strengthen task-completion guidance so the agent makes the requested change instead of declaring success when verification already passes.")
	case "protected_files_unchanged":
		return metricRecommendation(metric, "prompt", 15,
			"agent modified a file the scenario required leaving alone (trap violation)",
			"Strengthen investigate-before-act guidance: confirm a symbol is unused before removing it; do not change files outside the task's intent.")
	default:
		return metricRecommendation(metric, "eval-target", 80,
			"custom metric is below threshold",
			"Inspect the metric definition and failing scenarios, then decide whether the prompt, tool output, or scorer needs the next change.")
	}
}

func recommendationForRegression(metric MetricDelta) Recommendation {
	area := "regression"
	priority := 5
	action := "Compare current agent output against the baseline for scenarios where this metric dropped, then revert or refine the prompt/tool change that caused the regression."
	if strings.Contains(metric.Name, "verification") || strings.Contains(metric.Name, "final_answer") || strings.Contains(metric.Name, "false_file") {
		area = "prompt-regression"
	}
	if strings.Contains(metric.Name, "recorded") || strings.Contains(metric.Name, "journal") {
		area = "tool-output-regression"
	}
	return Recommendation{
		Area:     area,
		Priority: priority,
		Reason:   fmt.Sprintf("metric %q regressed %.1fpp", metric.Name, -metric.Delta*100),
		Action:   action,
	}
}

func metricRecommendation(metric MetricSummary, area string, priority int, reason, action string) Recommendation {
	return Recommendation{
		Area:     area,
		Priority: priority,
		Reason:   fmt.Sprintf("%s: %d/%d (%.1f%%)", reason, metric.Passed, metric.Total, metric.Ratio*100),
		Action:   action,
	}
}
