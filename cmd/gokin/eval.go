package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"gokin/internal/evals"

	"github.com/spf13/cobra"
)

func newEvalCmd() *cobra.Command {
	evalCmd := &cobra.Command{
		Use:   "eval",
		Short: "Run coding-agent evals",
	}
	evalCmd.AddCommand(newEvalRunCmd())
	evalCmd.AddCommand(newEvalReportCmd())
	evalCmd.AddCommand(newEvalDiagnoseCmd())
	evalCmd.AddCommand(newEvalValidateCmd())
	return evalCmd
}

func newEvalValidateCmd() *cobra.Command {
	var manifestPath string
	var fixturesRoot string
	var scenarioIDs []string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Verify every fixture honors its delivered-state contract",
		Long: `Verify the fixture contract for every scenario WITHOUT running any agent:
"red" fixtures (the default) must FAIL their verification commands as
delivered — the agent's job is to make them pass; "green" trap fixtures
must PASS — the agent's job is to not break them. A red fixture that
already passes measures nothing; this command catches that rot in CI.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			checks, err := evals.ValidateFixtures(cmd.Context(), evals.ValidateOptions{
				ManifestPath: manifestPath,
				FixturesRoot: fixturesRoot,
				ScenarioIDs:  scenarioIDs,
				Timeout:      timeout,
			})
			if err != nil {
				return err
			}

			failed := 0
			for _, check := range checks {
				status := "ok"
				if !check.OK {
					status = "BROKEN"
					failed++
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\texpect=%s\t%s\n", check.ScenarioID, status, check.Expect, check.Detail)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d/%d fixture contracts hold\n", len(checks)-failed, len(checks))
			if failed > 0 {
				return fmt.Errorf("eval validate failed: %d fixture contract(s) broken", failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "evals/coding/manifest.json", "eval manifest path")
	cmd.Flags().StringVar(&fixturesRoot, "fixtures", "evals/coding/fixtures", "fixture root directory")
	cmd.Flags().StringArrayVar(&scenarioIDs, "scenario", nil, "scenario id to validate; repeatable")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "timeout per verification command")
	return cmd
}

func newEvalRunCmd() *cobra.Command {
	var opts evals.RunOptions
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run coding eval scenarios from a manifest",
		Long: `Run coding eval scenarios by copying each fixture into an isolated workspace,
running an agent command template there, then executing scenario verification commands.

The agent command receives environment variables like GOKIN_EVAL_PROMPT,
GOKIN_EVAL_SCENARIO_ID, GOKIN_EVAL_PROVIDER, GOKIN_EVAL_MODEL,
GOKIN_EVAL_WORKSPACE, GOKIN_EVAL_FAULT_PROFILE, and GOKIN_EVAL_BASE_URL.
Template placeholders such as {{prompt}}, {{workspace}}, {{provider}}, {{model}},
{{fault_profile}}, {{base_url}}, and {{scenario_id}} are also supported.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Timeout = timeout
			results, err := evals.Run(cmd.Context(), opts)
			if err != nil {
				return err
			}

			passed := 0
			dryRun := 0
			failed := 0
			for _, result := range results {
				switch result.Status {
				case "passed":
					passed++
				case "dry_run":
					dryRun++
				default:
					failed++
				}
				status := result.Status
				if result.Error != "" {
					status += ": " + result.Error
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", evalResultLabel(result), status)
			}
			executed := len(results) - dryRun
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d/%d executed scenarios passed", passed, executed)
			if dryRun > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " · dry-run (not scored): %d", dryRun)
			}
			if opts.OutputPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), " · results: %s", opts.OutputPath)
			}
			fmt.Fprintln(cmd.OutOrStdout())

			if failed > 0 {
				return fmt.Errorf("eval run failed: %d/%d executed scenarios passed", passed, executed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.ManifestPath, "manifest", "evals/coding/manifest.json", "eval manifest path")
	cmd.Flags().StringVar(&opts.FixturesRoot, "fixtures", "evals/coding/fixtures", "fixture root directory")
	cmd.Flags().StringVar(&opts.WorkRoot, "workdir", "", "workspace root for copied fixtures (default: temp dir)")
	cmd.Flags().StringVar(&opts.OutputPath, "output", ".gokin/evals/results.jsonl", "JSONL output path")
	cmd.Flags().StringVar(&opts.AgentCommand, "agent-command", "", "shell command template to run in each fixture workspace")
	cmd.Flags().StringArrayVar(&opts.ScenarioIDs, "scenario", nil, "scenario id to run; repeatable")
	cmd.Flags().StringArrayVar(&opts.Providers, "provider", nil, "provider to include in the matrix; repeatable")
	cmd.Flags().StringArrayVar(&opts.Models, "model", nil, "model to include in the matrix; repeatable")
	cmd.Flags().StringArrayVar(&opts.FaultProfiles, "fault-profile", nil, "deterministic provider fault to inject once; repeatable")
	cmd.Flags().StringVar(&opts.FaultUpstream, "fault-upstream", "", "real provider base URL behind the loopback fault proxy")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "timeout per agent or verification command")
	cmd.Flags().BoolVar(&opts.KeepWorkspaces, "keep-workspaces", false, "keep temporary workspaces after the run")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "copy/list scenarios without running the agent command or verification")

	_ = cmd.RegisterFlagCompletionFunc("scenario", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		manifest, err := evals.LoadManifest(opts.ManifestPath)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		var ids []string
		for _, scenario := range manifest.Scenarios {
			if strings.HasPrefix(scenario.ID, toComplete) {
				ids = append(ids, scenario.ID)
			}
		}
		return ids, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("fault-profile", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var profiles []string
		for _, profile := range evals.FaultProfiles() {
			if strings.HasPrefix(profile, toComplete) {
				profiles = append(profiles, profile)
			}
		}
		return profiles, cobra.ShellCompDirectiveNoFileComp
	})

	cmd.SetContext(context.Background())
	return cmd
}

func evalResultLabel(result evals.Result) string {
	var parts []string
	if result.Provider != "" {
		parts = append(parts, result.Provider)
	}
	if result.Model != "" {
		parts = append(parts, result.Model)
	}
	if result.FaultProfile != "" {
		parts = append(parts, "fault="+result.FaultProfile)
	}
	if len(parts) == 0 {
		return result.ScenarioID
	}
	return fmt.Sprintf("%s [%s]", result.ScenarioID, strings.Join(parts, "/"))
}

func newEvalReportCmd() *cobra.Command {
	var inputPath string
	var baselinePath string
	var jsonOut bool
	var failUnder string
	var maxRegression string
	var requirePass bool
	var metricThresholds []string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarize eval JSONL results",
		Long: `Summarize eval JSONL results written by gokin eval run.

Use --baseline to compare the current run against a previous results file
after changing prompts, tools, routing, or model/provider settings.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			results, err := evals.ReadResults(inputPath)
			if err != nil {
				return fmt.Errorf("read input results: %w", err)
			}
			report := evals.BuildReport(inputPath, results)

			var comparison *evals.Comparison
			if strings.TrimSpace(baselinePath) != "" {
				baselineResults, err := evals.ReadResults(baselinePath)
				if err != nil {
					return fmt.Errorf("read baseline results: %w", err)
				}
				cmp := evals.CompareReports(evals.BuildReport(baselinePath, baselineResults), report)
				comparison = &cmp
			}
			if strings.TrimSpace(maxRegression) != "" && comparison == nil {
				return fmt.Errorf("--max-regression requires --baseline")
			}

			gateOpts, gateEnabled, err := evalGateOptions(failUnder, maxRegression, requirePass, metricThresholds)
			if err != nil {
				return err
			}
			var gate *evals.GateResult
			if gateEnabled {
				result := evals.EvaluateGate(report, comparison, gateOpts)
				gate = &result
			}

			if jsonOut {
				payload := struct {
					Report     evals.Report      `json:"report"`
					Comparison *evals.Comparison `json:"comparison,omitempty"`
					Gate       *evals.GateResult `json:"gate,omitempty"`
				}{Report: report, Comparison: comparison, Gate: gate}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(payload); err != nil {
					return err
				}
			} else {
				printEvalReport(cmd, report, comparison, gate)
			}

			if gate != nil && !gate.Passed {
				return fmt.Errorf("eval gate failed: %s", strings.Join(gate.Failures, "; "))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&inputPath, "input", ".gokin/evals/results.jsonl", "JSONL results path")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "optional baseline JSONL results path for comparison")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON")
	cmd.Flags().StringVar(&failUnder, "fail-under", "", "fail if aggregate score is below this ratio or percent (example: 0.9 or 90%)")
	cmd.Flags().StringVar(&maxRegression, "max-regression", "", "fail if score regresses by more than this ratio or percent-point value versus --baseline")
	cmd.Flags().BoolVar(&requirePass, "require-pass", false, "fail unless every scenario was actually executed and passed")
	cmd.Flags().StringArrayVar(&metricThresholds, "fail-metric", nil, "fail if metric ratio is below threshold, as name=ratio; repeatable")
	return cmd
}

func evalGateOptions(failUnder, maxRegression string, requirePass bool, metricThresholds []string) (evals.GateOptions, bool, error) {
	opts := evals.GateOptions{
		RequireAllPassed:    requirePass,
		FailOnMissingMetric: true,
	}
	enabled := requirePass
	var err error
	if strings.TrimSpace(failUnder) != "" {
		opts.MinScoreRatio, err = evals.ParseRatio(failUnder)
		if err != nil {
			return opts, false, fmt.Errorf("--fail-under: %w", err)
		}
		enabled = true
	}
	if strings.TrimSpace(maxRegression) != "" {
		opts.MaxRegression, err = evals.ParseRatio(maxRegression)
		if err != nil {
			return opts, false, fmt.Errorf("--max-regression: %w", err)
		}
		opts.RequireComparableBaseline = true
		enabled = true
	}
	opts.MetricMinRatios, err = evals.ParseMetricThresholds(metricThresholds)
	if err != nil {
		return opts, false, fmt.Errorf("--fail-metric: %w", err)
	}
	if len(opts.MetricMinRatios) > 0 {
		enabled = true
	}
	return opts, enabled, nil
}

func printEvalReport(cmd *cobra.Command, report evals.Report, comparison *evals.Comparison, gate *evals.GateResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Results: %s\n", report.ResultsPath)
	fmt.Fprintf(out, "Scenarios: %d · passed: %d · failed: %d · dry-run: %d · score: %d/%d (%.1f%%)\n",
		report.Count, report.Passed, report.Failed, report.DryRun, report.Score.Passed, report.Score.Total, report.Score.Ratio*100)

	if len(report.Metrics) > 0 {
		fmt.Fprintln(out, "\nMetrics:")
		for _, metric := range report.Metrics {
			fmt.Fprintf(out, "  %-36s %d/%d (%.1f%%)\n", metric.Name, metric.Passed, metric.Total, metric.Ratio*100)
		}
	}

	var failing []evals.ScenarioSummary
	for _, scenario := range report.Scenarios {
		if scenario.Status != "passed" && scenario.Status != "dry_run" {
			failing = append(failing, scenario)
		}
	}
	if len(failing) > 0 {
		fmt.Fprintln(out, "\nFailing scenarios:")
		for _, scenario := range failing {
			label := scenario.ID
			if scenario.Variant != "" {
				label += " [" + scenario.Variant + "]"
			}
			fmt.Fprintf(out, "  %s\t%s\t%d/%d", label, scenario.Status, scenario.Score.Passed, scenario.Score.Total)
			if scenario.Error != "" {
				fmt.Fprintf(out, "\t%s", scenario.Error)
			}
			fmt.Fprintln(out)
		}
	}

	if comparison != nil {
		fmt.Fprintf(out, "\nBaseline: %s\n", comparison.BaselinePath)
		comparable := comparison.CohortMismatch == nil && comparison.InvalidEvidence == nil
		if comparison.InvalidEvidence != nil {
			fmt.Fprintf(out, "Comparison unavailable: invalid evidence (empty baseline=%t/current=%t; dry-run=%d baseline/%d current; not-executed=%d baseline/%d current)\n",
				comparison.InvalidEvidence.BaselineEmpty, comparison.InvalidEvidence.CurrentEmpty,
				comparison.InvalidEvidence.BaselineDryRun, comparison.InvalidEvidence.CurrentDryRun,
				comparison.InvalidEvidence.BaselineNotExecuted, comparison.InvalidEvidence.CurrentNotExecuted)
		}
		if comparison.CohortMismatch != nil {
			fmt.Fprintf(out, "Comparison unavailable: cohort mismatch (%d baseline-only, %d current-only, %d duplicate baseline, %d duplicate current, %d changed spec)\n",
				len(comparison.CohortMismatch.BaselineOnly), len(comparison.CohortMismatch.CurrentOnly),
				len(comparison.CohortMismatch.BaselineDuplicates), len(comparison.CohortMismatch.CurrentDuplicates),
				len(comparison.CohortMismatch.SpecMismatches))
			printEvalScenarioIdentities(out, "Baseline only", comparison.CohortMismatch.BaselineOnly)
			printEvalScenarioIdentities(out, "Current only", comparison.CohortMismatch.CurrentOnly)
			printEvalScenarioIdentities(out, "Duplicate baseline rows", comparison.CohortMismatch.BaselineDuplicates)
			printEvalScenarioIdentities(out, "Duplicate current rows", comparison.CohortMismatch.CurrentDuplicates)
			printEvalScenarioIdentities(out, "Changed scenario specs", comparison.CohortMismatch.SpecMismatches)
		}
		if comparable {
			fmt.Fprintf(out, "Delta: passed %+d · score %+0.1fpp\n", comparison.PassedDelta, comparison.ScoreDelta*100)
		}
		if comparable && len(comparison.Metrics) > 0 {
			fmt.Fprintln(out, "\nMetric deltas:")
			for _, metric := range comparison.Metrics {
				if metric.Delta == 0 {
					continue
				}
				fmt.Fprintf(out, "  %-36s %+0.1fpp (%.1f%% -> %.1f%%)\n",
					metric.Name, metric.Delta*100, metric.BaselineRatio*100, metric.CurrentRatio*100)
			}
		}
	}

	if gate != nil {
		if gate.Passed {
			fmt.Fprintln(out, "\nGate: passed")
			return
		}
		fmt.Fprintln(out, "\nGate: failed")
		for _, failure := range gate.Failures {
			fmt.Fprintf(out, "  - %s\n", failure)
		}
	}
}

func printEvalScenarioIdentities(out io.Writer, heading string, identities []evals.ScenarioIdentity) {
	if len(identities) == 0 {
		return
	}
	fmt.Fprintf(out, "%s:\n", heading)
	for _, identity := range identities {
		label := identity.ID
		if identity.Variant != "" {
			label += " [" + identity.Variant + "]"
		}
		fmt.Fprintf(out, "  - %s\n", label)
	}
}

func newEvalDiagnoseCmd() *cobra.Command {
	var inputPath string
	var baselinePath string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Recommend prompt/tool improvements from eval results",
		Long: `Diagnose eval JSONL results and turn weak metrics into prioritized
next actions for the prompt/tool improvement loop.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			results, err := evals.ReadResults(inputPath)
			if err != nil {
				return fmt.Errorf("read input results: %w", err)
			}
			report := evals.BuildReport(inputPath, results)

			var comparison *evals.Comparison
			if strings.TrimSpace(baselinePath) != "" {
				baselineResults, err := evals.ReadResults(baselinePath)
				if err != nil {
					return fmt.Errorf("read baseline results: %w", err)
				}
				cmp := evals.CompareReports(evals.BuildReport(baselinePath, baselineResults), report)
				comparison = &cmp
			}

			diagnosis := evals.DiagnoseReport(report, comparison)
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(diagnosis)
			}
			printEvalDiagnosis(cmd, diagnosis)
			return nil
		},
	}

	cmd.Flags().StringVar(&inputPath, "input", ".gokin/evals/results.jsonl", "JSONL results path")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "optional baseline JSONL results path for regression diagnosis")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON")
	return cmd
}

func printEvalDiagnosis(cmd *cobra.Command, diagnosis evals.Diagnosis) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Results: %s\n", diagnosis.ResultsPath)
	fmt.Fprintf(out, "Score: %d/%d (%.1f%%)\n", diagnosis.Score.Passed, diagnosis.Score.Total, diagnosis.Score.Ratio*100)
	if diagnosis.DryRun > 0 {
		fmt.Fprintf(out, "Dry-run scenarios (not scored): %d\n", diagnosis.DryRun)
	}
	if diagnosis.CohortMismatch != nil {
		fmt.Fprintf(out, "Comparison cohort mismatch: %d baseline-only, %d current-only, %d duplicate baseline, %d duplicate current, %d changed spec\n",
			len(diagnosis.CohortMismatch.BaselineOnly), len(diagnosis.CohortMismatch.CurrentOnly),
			len(diagnosis.CohortMismatch.BaselineDuplicates), len(diagnosis.CohortMismatch.CurrentDuplicates),
			len(diagnosis.CohortMismatch.SpecMismatches))
	}
	if diagnosis.InvalidEvidence != nil {
		fmt.Fprintf(out, "Comparison invalid evidence: empty baseline=%t/current=%t; dry-run=%d baseline/%d current; not-executed=%d baseline/%d current\n",
			diagnosis.InvalidEvidence.BaselineEmpty, diagnosis.InvalidEvidence.CurrentEmpty,
			diagnosis.InvalidEvidence.BaselineDryRun, diagnosis.InvalidEvidence.CurrentDryRun,
			diagnosis.InvalidEvidence.BaselineNotExecuted, diagnosis.InvalidEvidence.CurrentNotExecuted)
	}

	if len(diagnosis.WeakMetrics) > 0 {
		fmt.Fprintln(out, "\nWeak metrics:")
		for _, metric := range diagnosis.WeakMetrics {
			fmt.Fprintf(out, "  %-36s %d/%d (%.1f%%)\n", metric.Name, metric.Passed, metric.Total, metric.Ratio*100)
		}
	}

	if len(diagnosis.Regressions) > 0 {
		fmt.Fprintln(out, "\nRegressions:")
		for _, metric := range diagnosis.Regressions {
			fmt.Fprintf(out, "  %-36s %+0.1fpp (%.1f%% -> %.1f%%)\n",
				metric.Name, metric.Delta*100, metric.BaselineRatio*100, metric.CurrentRatio*100)
		}
	}

	if len(diagnosis.FailedScenarios) > 0 {
		fmt.Fprintln(out, "\nFailed scenarios:")
		for _, scenario := range diagnosis.FailedScenarios {
			label := scenario.ID
			if scenario.Variant != "" {
				label += " [" + scenario.Variant + "]"
			}
			fmt.Fprintf(out, "  %s\t%s\t%d/%d\n", label, scenario.Status, scenario.Score.Passed, scenario.Score.Total)
		}
	}

	fmt.Fprintln(out, "\nRecommended next actions:")
	for _, rec := range diagnosis.Recommendations {
		fmt.Fprintf(out, "  [%s] %s\n", rec.Area, rec.Reason)
		fmt.Fprintf(out, "      %s\n", rec.Action)
	}
}
