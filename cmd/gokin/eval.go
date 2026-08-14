package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
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
	evalCmd.AddCommand(newEvalBaselineAuditCmd())
	return evalCmd
}

func newEvalBaselineAuditCmd() *cobra.Command {
	var manifestPath string
	var inputPaths []string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "baseline-audit",
		Short: "Check baseline coverage against the current eval manifest",
		Long: `Check that every provider/model cohort in each baseline JSONL file contains
exactly one result for every scenario in the current manifest. This detects
stale baselines after scenarios are added, plus unknown or duplicate rows,
without running an agent or spending provider tokens.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if len(inputPaths) == 0 {
				return fmt.Errorf("at least one --input baseline JSONL path is required")
			}
			audits := make([]evals.BaselineCoverage, 0, len(inputPaths))
			complete := true
			for _, inputPath := range inputPaths {
				audit, err := evals.AuditBaselineCoverage(manifestPath, inputPath)
				if err != nil {
					return fmt.Errorf("audit %s: %w", inputPath, err)
				}
				complete = complete && audit.Complete
				audits = append(audits, audit)
			}
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(audits); err != nil {
					return err
				}
			} else {
				for _, audit := range audits {
					fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", audit.InputPath)
					for _, variant := range audit.Variants {
						status := "complete"
						if !variant.Complete {
							status = "STALE"
						}
						fmt.Fprintf(cmd.OutOrStdout(), "  %s: %d/%d · %s\n",
							variant.Variant, variant.Present, variant.Expected, status)
						if len(variant.Missing) > 0 {
							fmt.Fprintf(cmd.OutOrStdout(), "    missing: %s\n",
								strings.Join(variant.Missing, ", "))
						}
						if len(variant.Unknown) > 0 {
							fmt.Fprintf(cmd.OutOrStdout(), "    unknown: %s\n",
								strings.Join(variant.Unknown, ", "))
						}
						if len(variant.Duplicates) > 0 {
							fmt.Fprintf(cmd.OutOrStdout(), "    duplicates: %s\n",
								strings.Join(variant.Duplicates, ", "))
						}
					}
				}
			}
			if !complete {
				return fmt.Errorf("baseline audit failed: one or more cohorts do not match the current manifest")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&manifestPath, "manifest", "evals/coding/manifest.json", "eval manifest path")
	cmd.Flags().StringArrayVar(&inputPaths, "input", nil, "baseline JSONL results path; repeatable")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print machine-readable JSON")
	return cmd
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
GOKIN_EVAL_ENGINE_MODE, GOKIN_ENGINE_MODE, GOKIN_EVAL_WORKSPACE,
GOKIN_EVAL_TRIAL, GOKIN_EVAL_TRIAL_COUNT, GOKIN_EVAL_FAULT_PROFILE,
and GOKIN_EVAL_BASE_URL. GOKIN_EVAL_RUNTIME_DIR is reserved for the trusted
headless runtime journal and must not be forwarded to model-executed commands.
Template placeholders such as {{prompt}}, {{workspace}}, {{provider}}, {{model}},
		{{engine_mode}}, {{trial}}, {{trial_count}}, {{fault_profile}}, {{base_url}}, and
		{{scenario_id}} are also supported.

Completed rows are durably checkpointed to OUTPUT.partial while the previous
OUTPUT remains untouched. A complete run atomically publishes OUTPUT. After an
interruption, repeat the exact command with --resume; changed matrix, scenario,
fixture, command, timeout, or explicitly selected GOKIN_BIN is rejected.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Repeat < 1 || opts.Repeat > 100 {
				return fmt.Errorf("--repeat must be between 1 and 100")
			}
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
	cmd.Flags().StringArrayVar(&opts.EngineModes, "engine-mode", nil, "engine mode to include (auto, tools, hybrid); repeatable; default auto")
	cmd.Flags().IntVar(&opts.Repeat, "repeat", 1, "repeat the complete matrix with isolated, paired trials (1-100)")
	cmd.Flags().StringArrayVar(&opts.FaultProfiles, "fault-profile", nil, "deterministic provider fault to inject once; repeatable")
	cmd.Flags().StringVar(&opts.FaultUpstream, "fault-upstream", "", "real provider base URL behind the loopback fault proxy")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "timeout per agent or verification command")
	cmd.Flags().BoolVar(&opts.KeepWorkspaces, "keep-workspaces", false, "keep temporary workspaces after the run")
	cmd.Flags().BoolVar(&opts.Resume, "resume", false, "resume the exact interrupted run from OUTPUT.partial")
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
	_ = cmd.RegisterFlagCompletionFunc("engine-mode", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		var modes []string
		for _, mode := range []string{"auto", "tools", "hybrid"} {
			if strings.HasPrefix(mode, toComplete) {
				modes = append(modes, mode)
			}
		}
		return modes, cobra.ShellCompDirectiveNoFileComp
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
	if result.EngineMode != "" {
		parts = append(parts, "engine="+result.EngineMode)
	}
	if result.FaultProfile != "" {
		parts = append(parts, "fault="+result.FaultProfile)
	}
	if result.Trial > 0 {
		trial := fmt.Sprintf("trial=%d", result.Trial)
		if result.TrialCount > 0 {
			trial += fmt.Sprintf("/%d", result.TrialCount)
		}
		parts = append(parts, trial)
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
	var engineGateModes []string
	var requireCompleteEnginePairs bool
	var maxEngineScoreRegression string
	var maxEngineQualityRegressions int
	var maxEngineRelativeDeltas []string
	var maxEngineMedianRelativeDeltas []string
	var minEngineLowerRatios []string
	var maxEngineLowerPValues []string
	var minEngineReplUseRatios []string
	var maxEngineReplUseRatios []string

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Summarize eval JSONL results",
		Long: `Summarize eval JSONL results written by gokin eval run.

Use --baseline to compare the current run against a previous results file
after changing prompts, tools, routing, or model/provider settings. Engine gate
flags evaluate paired tools/auto/hybrid rows from the same results file and do
not require an external baseline.`,
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
			engineGateEnabled, err := applyEngineGateOptions(&gateOpts, evalEngineGateFlags{
				Modes: engineGateModes, RequireCompletePairs: requireCompleteEnginePairs,
				MaxScoreRegression: maxEngineScoreRegression, MaxQualityRegressions: maxEngineQualityRegressions,
				MaxRelativeDeltas: maxEngineRelativeDeltas, MaxMedianRelativeDeltas: maxEngineMedianRelativeDeltas,
				MinLowerRatios:   minEngineLowerRatios,
				MaxLowerPValues:  maxEngineLowerPValues,
				MinReplUseRatios: minEngineReplUseRatios, MaxReplUseRatios: maxEngineReplUseRatios,
			})
			if err != nil {
				return err
			}
			gateEnabled = gateEnabled || engineGateEnabled
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
	cmd.Flags().StringArrayVar(&engineGateModes, "engine-gate-mode", nil, "engine comparison to gate (auto or hybrid); repeatable; default auto when an engine gate is enabled")
	cmd.Flags().BoolVar(&requireCompleteEnginePairs, "require-complete-engine-pairs", false, "fail unless selected comparisons have valid, fully measured pairs, complete mode-provenant and exposure-consistent hybrid policy evidence, and zero exclusions")
	cmd.Flags().StringVar(&maxEngineScoreRegression, "max-engine-score-regression", "", "maximum paired score regression for all/candidate/control cohorts (example: 0 or 2%)")
	cmd.Flags().IntVar(&maxEngineQualityRegressions, "max-engine-quality-regressions", -1, "maximum selected-engine scenario regressions; disabled when negative")
	cmd.Flags().StringArrayVar(&maxEngineRelativeDeltas, "max-engine-relative-delta", nil, "maximum paired efficiency delta as cohort.metric=value; repeatable")
	cmd.Flags().StringArrayVar(&maxEngineMedianRelativeDeltas, "max-engine-median-relative-delta", nil, "maximum trial-clustered median relative efficiency delta as cohort.metric=value; repeatable")
	cmd.Flags().StringArrayVar(&minEngineLowerRatios, "min-engine-lower-ratio", nil, "minimum share of trial-clustered evidence units lower than tools as cohort.metric=ratio; repeatable")
	cmd.Flags().StringArrayVar(&maxEngineLowerPValues, "max-engine-lower-p-value", nil, "maximum one-sided exact sign-test p-value as cohort.metric=value; repeatable; trials are clustered")
	cmd.Flags().StringArrayVar(&minEngineReplUseRatios, "min-engine-repl-use-ratio", nil, "minimum share of current-engine pairs that call repl_exec as cohort=ratio; repeatable")
	cmd.Flags().StringArrayVar(&maxEngineReplUseRatios, "max-engine-repl-use-ratio", nil, "maximum share of current-engine pairs that call repl_exec as cohort=ratio; repeatable")
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

type evalEngineGateFlags struct {
	Modes                   []string
	RequireCompletePairs    bool
	MaxScoreRegression      string
	MaxQualityRegressions   int
	MaxRelativeDeltas       []string
	MaxMedianRelativeDeltas []string
	MinLowerRatios          []string
	MaxLowerPValues         []string
	MinReplUseRatios        []string
	MaxReplUseRatios        []string
}

func applyEngineGateOptions(opts *evals.GateOptions, flags evalEngineGateFlags) (bool, error) {
	if opts == nil {
		return false, fmt.Errorf("engine gate options target is nil")
	}
	modes := make([]string, 0, len(flags.Modes))
	seen := make(map[string]bool, len(flags.Modes))
	for _, rawMode := range flags.Modes {
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		if mode == "" {
			continue
		}
		if mode != "auto" && mode != "hybrid" {
			return false, fmt.Errorf("--engine-gate-mode %q: expected auto or hybrid", rawMode)
		}
		if !seen[mode] {
			seen[mode] = true
			modes = append(modes, mode)
		}
	}
	if flags.MaxQualityRegressions < -1 {
		return false, fmt.Errorf("--max-engine-quality-regressions must be non-negative or omitted")
	}
	maxRelativeDeltas, err := evals.ParseEngineMaxRelativeDeltas(flags.MaxRelativeDeltas)
	if err != nil {
		return false, fmt.Errorf("--max-engine-relative-delta: %w", err)
	}
	maxMedianRelativeDeltas, err := evals.ParseEngineMaxMedianRelativeDeltas(flags.MaxMedianRelativeDeltas)
	if err != nil {
		return false, fmt.Errorf("--max-engine-median-relative-delta: %w", err)
	}
	minLowerRatios, err := evals.ParseEngineMinLowerRatios(flags.MinLowerRatios)
	if err != nil {
		return false, fmt.Errorf("--min-engine-lower-ratio: %w", err)
	}
	maxLowerPValues, err := evals.ParseEngineMaxLowerPValues(flags.MaxLowerPValues)
	if err != nil {
		return false, fmt.Errorf("--max-engine-lower-p-value: %w", err)
	}
	minReplUseRatios, err := evals.ParseEngineReplUseRatios(flags.MinReplUseRatios)
	if err != nil {
		return false, fmt.Errorf("--min-engine-repl-use-ratio: %w", err)
	}
	maxReplUseRatios, err := evals.ParseEngineReplUseRatios(flags.MaxReplUseRatios)
	if err != nil {
		return false, fmt.Errorf("--max-engine-repl-use-ratio: %w", err)
	}
	enabled := flags.RequireCompletePairs || strings.TrimSpace(flags.MaxScoreRegression) != "" ||
		flags.MaxQualityRegressions >= 0 || len(maxRelativeDeltas) > 0 || len(maxMedianRelativeDeltas) > 0 || len(minLowerRatios) > 0 ||
		len(maxLowerPValues) > 0 ||
		len(minReplUseRatios) > 0 || len(maxReplUseRatios) > 0
	if !enabled {
		return false, nil
	}
	if len(modes) == 0 {
		modes = []string{"auto"}
	}
	opts.EngineModes = modes
	opts.RequireCompleteEnginePairs = flags.RequireCompletePairs
	if strings.TrimSpace(flags.MaxScoreRegression) != "" {
		ratio, err := evals.ParseRatio(flags.MaxScoreRegression)
		if err != nil {
			return false, fmt.Errorf("--max-engine-score-regression: %w", err)
		}
		opts.MaxEngineScoreRegression = &ratio
	}
	if flags.MaxQualityRegressions >= 0 {
		limit := flags.MaxQualityRegressions
		opts.MaxEngineQualityRegressions = &limit
	}
	opts.EngineMaxRelativeDeltas = maxRelativeDeltas
	opts.EngineMaxMedianRelativeDeltas = maxMedianRelativeDeltas
	opts.EngineMinLowerRatios = minLowerRatios
	opts.EngineMaxLowerPValues = maxLowerPValues
	opts.EngineMinReplUseRatios = minReplUseRatios
	opts.EngineMaxReplUseRatios = maxReplUseRatios
	return true, nil
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

	if len(report.ToolUsage) > 0 {
		fmt.Fprintln(out, "\nTools chosen (scenarios that used each at least once):")
		for _, tool := range report.ToolUsage {
			fmt.Fprintf(out, "  %-36s %d (%.0f%%)\n", tool.Name, tool.Scenarios, tool.Ratio*100)
		}
	}

	if len(report.Engines) > 0 {
		fmt.Fprintln(out, "\nEngine efficiency:")
		for _, engine := range report.Engines {
			eff := engine.Efficiency
			if eff.MeasuredScenarios == 0 {
				fmt.Fprintf(out, "  %-10s score %d/%d (%.1f%%) · trusted journal %d/%d · no headless metrics · policy %d · mode %d · mode mismatch %d · aligned %d · gaps %d · unexpected %d · eligible %d · exposed %d · used %d · repl calls %d · scan ops %d · index refreshes %d · efficient %d/%d (misses %d)%s%s\n",
					engine.Mode, engine.Score.Passed, engine.Score.Total, engine.Score.Ratio*100,
					eff.TrustedRuntimeScenarios, engine.Passed+engine.Failed,
					eff.HybridPolicyObserved, eff.HybridModeMatched, eff.HybridModeMismatches,
					eff.HybridExposureMatched, eff.HybridExposureGaps,
					eff.HybridUnexpectedExposure, eff.HybridEligible, eff.ReplExposed,
					eff.ReplUsedScenarios, eff.ReplCalls, eff.ReplScanOperations, eff.ReplFileIndexRefreshes, eff.EfficientPathMatched,
					eff.EfficientPathExpected, eff.EfficientPathMisses, formatCountMap("operations", eff.ReplOperations),
					formatCountMap("strategies", eff.HybridStrategies))
				continue
			}
			n := eff.MeasuredScenarios
			fmt.Fprintf(out, "  %-10s score %d/%d (%.1f%%) · trusted journal %d/%d · avg tokens %d%s · rounds %.1f · duration %.1fs · policy %d · mode %d · mode mismatch %d · aligned %d · gaps %d · unexpected %d · eligible %d · exposed %d · used %d · repl calls %d · scan ops %d · index refreshes %d · efficient %d/%d (misses %d)%s%s\n",
				engine.Mode, engine.Score.Passed, engine.Score.Total, engine.Score.Ratio*100,
				eff.TrustedRuntimeScenarios, engine.Passed+engine.Failed,
				eff.TotalTokens/n, formatEngineTokenBreakdown(eff), float64(eff.ModelRounds)/float64(n),
				float64(eff.DurationMillis)/float64(n)/1000, eff.HybridPolicyObserved,
				eff.HybridModeMatched, eff.HybridModeMismatches,
				eff.HybridExposureMatched, eff.HybridExposureGaps, eff.HybridUnexpectedExposure,
				eff.HybridEligible, eff.ReplExposed, eff.ReplUsedScenarios, eff.ReplCalls, eff.ReplScanOperations, eff.ReplFileIndexRefreshes,
				eff.EfficientPathMatched, eff.EfficientPathExpected, eff.EfficientPathMisses,
				formatCountMap("operations", eff.ReplOperations), formatCountMap("strategies", eff.HybridStrategies))
		}
	}
	if len(report.EngineComparisons) > 0 {
		fmt.Fprintln(out, "\nPaired engine deltas vs tools (negative efficiency deltas are better):")
		for _, comparison := range report.EngineComparisons {
			printEngineCohortDelta(out, comparison.Mode, "all", comparison.All)
			if comparison.Candidates.Pairs > 0 {
				printEngineCohortDelta(out, comparison.Mode, "candidates", comparison.Candidates)
			}
			if comparison.Controls.Pairs > 0 {
				printEngineCohortDelta(out, comparison.Mode, "controls", comparison.Controls)
			}
			fmt.Fprintf(out, "    provenance: paired %d/%d · scenario spec %d/%d · run spec %d/%d · classification %d/%d\n",
				comparison.Provenance.Pairs, comparison.All.Pairs,
				comparison.Provenance.ScenarioSpecVerified, comparison.All.Pairs,
				comparison.Provenance.RunSpecVerified, comparison.All.Pairs,
				comparison.Provenance.ClassificationVerified, comparison.All.Pairs)
			excluded := comparison.Excluded
			if excluded.Total() > 0 {
				fmt.Fprintf(out, "    excluded: baseline-only %d · current-only %d · duplicates %d · non-executed %d · spec mismatch %d · run-spec mismatch %d · classification mismatch %d\n",
					excluded.BaselineOnly, excluded.CurrentOnly, excluded.DuplicateCohorts,
					excluded.NonExecuted, excluded.SpecMismatches, excluded.RunSpecMismatches, excluded.ClassificationMismatches)
			}
		}
	}
	if len(report.Engines) > 1 {
		rows := make([]evals.ScenarioSummary, 0, len(report.Scenarios))
		for _, scenario := range report.Scenarios {
			if scenario.TotalTokens > 0 || scenario.ModelRounds > 0 || scenario.ReplCalls > 0 {
				rows = append(rows, scenario)
			}
		}
		if len(rows) > 0 {
			fmt.Fprintln(out, "\nScenario efficiency:")
			for _, scenario := range rows {
				label := scenario.ID
				if scenario.Variant != "" {
					label += " [" + scenario.Variant + "]"
				}
				duration := scenario.AgentDuration
				if duration == 0 {
					duration = scenario.Duration
				}
				fmt.Fprintf(out, "  %-56s tokens %d · rounds %d · duration %.1fs · repl %d · scan ops %d · index refreshes %d\n",
					label, scenario.TotalTokens, scenario.ModelRounds,
					float64(duration)/1000, scenario.ReplCalls, scenario.ReplScanOperations, scenario.ReplFileIndexRefreshes)
			}
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

func formatEngineTokenBreakdown(eff evals.EfficiencySummary) string {
	n := eff.TokenBreakdownScenarios
	if n == 0 {
		return fmt.Sprintf(" · token breakdown 0/%d", eff.MeasuredScenarios)
	}
	return fmt.Sprintf(" · avg input %d (uncached %d, cache read %d) · avg output %d · token breakdown %d/%d",
		eff.InputTokens/n, eff.UncachedInputTokens/n, eff.CacheReadInputTokens/n,
		eff.OutputTokens/n, n, eff.MeasuredScenarios)
}

func printEngineCohortDelta(out io.Writer, mode, cohort string, delta evals.EngineCohortComparison) {
	if delta.Pairs == 0 {
		fmt.Fprintf(out, "  %-10s %-10s no valid pairs\n", mode, cohort)
		return
	}
	fmt.Fprintf(out, "  %-10s %-10s pairs %d · pass %+d · score %+.1fpp",
		mode, cohort, delta.Pairs, delta.PassedDelta, delta.ScoreDelta*100)
	fmt.Fprintf(out, "\n    hybrid: trusted runtime %d/%d · policy %d/%d · mode %d/%d · mode mismatch %d · aligned %d/%d · gaps %d · unexpected %d · eligible %d/%d · exposed %d/%d · used %d/%d (%.1f%%) · calls %d · scan ops %d · index refreshes %d · efficient %d/%d (misses %d)",
		delta.Hybrid.TrustedRuntime, delta.Pairs, delta.Hybrid.PolicyObserved, delta.Pairs, delta.Hybrid.ModeMatched, delta.Pairs,
		delta.Hybrid.ModeMismatches, delta.Hybrid.ExposureMatched, delta.Pairs,
		delta.Hybrid.ExposureGaps, delta.Hybrid.UnexpectedExposure, delta.Hybrid.Eligible, delta.Pairs,
		delta.Hybrid.Exposed, delta.Pairs, delta.Hybrid.Used, delta.Pairs,
		delta.Hybrid.UseRatio*100, delta.Hybrid.Calls, delta.Hybrid.ScanOperations, delta.Hybrid.FileIndexRefreshes, delta.Hybrid.EfficientMatched,
		delta.Hybrid.EfficientExpected, delta.Hybrid.EfficientMisses)
	fmt.Fprint(out, formatCountMap("strategies", delta.Hybrid.Strategies))
	if delta.Efficiency.TotalTokens.Pairs > 0 {
		fmt.Fprint(out, "\n")
		printPairedMetric(out, "tokens", delta.Efficiency.TotalTokens, 1, "")
		if delta.Efficiency.InputTokens.Pairs > 0 {
			printPairedMetric(out, "input", delta.Efficiency.InputTokens, 1, "")
			printPairedMetric(out, "uncached", delta.Efficiency.UncachedInputTokens, 1, "")
			printPairedMetric(out, "cache read", delta.Efficiency.CacheReadInputTokens, 1, "")
			printPairedMetric(out, "output", delta.Efficiency.OutputTokens, 1, "")
		} else {
			fmt.Fprintf(out, "    token breakdown unavailable for paired rows\n")
		}
		printPairedMetric(out, "rounds", delta.Efficiency.ModelRounds, 1, "")
		printPairedMetric(out, "duration", scalePairedMetric(delta.Efficiency.DurationMillis, 1000), 1, "s")
		printPairedMetric(out, "repl", delta.Efficiency.ReplCalls, 1, "")
		if delta.Efficiency.EstimatedUSD.Pairs > 0 {
			printPairedMetric(out, "cost USD", delta.Efficiency.EstimatedUSD, 4, "")
		}
	} else {
		fmt.Fprint(out, " · no paired headless metrics\n")
	}
	fmt.Fprintf(out, "    quality: regressions %d · improvements %d\n",
		len(delta.QualityRegressions), len(delta.QualityImprovements))
	printEvalScenarioIdentities(out, "    quality regressions", delta.QualityRegressions)
}

func formatCountMap(label string, counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, counts[name]))
	}
	return " · " + label + " " + strings.Join(parts, ",")
}

func formatREPLOperations(operations map[string]int) string {
	return formatCountMap("operations", operations)
}

func printPairedMetric(out io.Writer, name string, metric evals.PairedMetricComparison, precision int, suffix string) {
	fmt.Fprintf(out, "    %-10s %.*f → %.*f (avg Δ %+.*f%s, pair median Δ %+.*f%s",
		name, precision, metric.BaselineAverage, precision, metric.CurrentAverage,
		precision, metric.AverageDelta, suffix, precision, metric.MedianDelta, suffix)
	if metric.EvidenceUnits > 0 {
		fmt.Fprintf(out, ", unit median Δ %+.*f%s", precision, metric.ClusteredMedianDelta, suffix)
	}
	if metric.RelativeDelta != nil {
		fmt.Fprintf(out, ", aggregate %+.1f%%", *metric.RelativeDelta*100)
	}
	if metric.MedianRelativeDelta != nil {
		fmt.Fprintf(out, ", pair median %+.1f%%", *metric.MedianRelativeDelta*100)
		if metric.RelativePairs != metric.Pairs {
			fmt.Fprintf(out, " over %d/%d nonzero baselines", metric.RelativePairs, metric.Pairs)
		}
	}
	if metric.ClusteredMedianRelativeDelta != nil {
		fmt.Fprintf(out, ", unit median %+.1f%% over %d/%d units",
			*metric.ClusteredMedianRelativeDelta*100,
			metric.ClusteredRelativeEvidenceUnits, metric.EvidenceUnits)
	}
	fmt.Fprintf(out, ") · pairs lower/equal/higher %d/%d/%d · units %d: %d/%d/%d",
		metric.Lower, metric.Equal, metric.Higher,
		metric.EvidenceUnits, metric.UnitLower, metric.UnitEqual, metric.UnitHigher)
	if metric.LowerSignTestPValue != nil {
		fmt.Fprintf(out, " · lower sign p=%.4g", *metric.LowerSignTestPValue)
	}
	fmt.Fprintln(out)
}

func scalePairedMetric(metric evals.PairedMetricComparison, divisor float64) evals.PairedMetricComparison {
	metric.BaselineAverage /= divisor
	metric.CurrentAverage /= divisor
	metric.AverageDelta /= divisor
	metric.MedianDelta /= divisor
	metric.ClusteredMedianDelta /= divisor
	return metric
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
