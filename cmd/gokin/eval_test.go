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
	for _, flag := range []string{"manifest", "fixtures", "workdir", "output", "agent-command", "timeout", "dry-run"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag %q not registered", flag)
		}
	}
}

func TestNewEvalReportCmd_FlagsRegistered(t *testing.T) {
	cmd := newEvalReportCmd()
	for _, flag := range []string{"input", "baseline", "json", "fail-under", "max-regression", "require-pass", "fail-metric"} {
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
