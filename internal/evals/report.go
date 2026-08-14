package evals

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Report summarizes one eval results JSONL file.
type Report struct {
	ResultsPath string          `json:"results_path,omitempty"`
	Count       int             `json:"count"`
	Passed      int             `json:"passed"`
	DryRun      int             `json:"dry_run"`
	Failed      int             `json:"failed"`
	Score       ScoreSummary    `json:"score"`
	Metrics     []MetricSummary `json:"metrics"`
	// ToolUsage records which tools the agent actually reached for, across the
	// scenarios that executed. A tool can be registered, permitted, and
	// advertised while never being chosen — that is invisible in pass rates and
	// metric ratios, and finding it previously meant reading raw journals by
	// hand. Reporting it turns "is this tool earning its place in the schema?"
	// into something every run answers on its own.
	ToolUsage []ToolUsageSummary `json:"tool_usage,omitempty"`
	Engines   []EngineSummary    `json:"engines,omitempty"`
	// EngineComparisons contains paired, cohort-safe deltas. Unlike Engines,
	// these rows only compare results that share scenario, provider, model,
	// fault profile, and scenario specification.
	EngineComparisons []EngineComparison `json:"engine_comparisons,omitempty"`
	Scenarios         []ScenarioSummary  `json:"scenarios"`
}

// EngineSummary aggregates each mode independently. Use EngineComparisons for
// cohort-safe A/B conclusions when a tools/auto/hybrid matrix is present.
type EngineSummary struct {
	Mode       string            `json:"mode"`
	Count      int               `json:"count"`
	Passed     int               `json:"passed"`
	Failed     int               `json:"failed"`
	Score      ScoreSummary      `json:"score"`
	Efficiency EfficiencySummary `json:"efficiency"`
}

type EfficiencySummary struct {
	TrustedRuntimeScenarios  int            `json:"trusted_runtime_scenarios"`
	MeasuredScenarios        int            `json:"measured_scenarios"`
	TokenBreakdownScenarios  int            `json:"token_breakdown_scenarios"`
	InputTokens              int            `json:"input_tokens"`
	UncachedInputTokens      int            `json:"uncached_input_tokens"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	TotalTokens              int            `json:"total_tokens"`
	ModelRounds              int            `json:"model_rounds"`
	DurationMillis           int64          `json:"duration_ms"`
	ReplCalls                int            `json:"repl_calls"`
	ReplUsedScenarios        int            `json:"repl_used_scenarios"`
	ReplOperations           map[string]int `json:"repl_operations,omitempty"`
	ReplScanOperations       int            `json:"repl_scan_operations"`
	ReplFileIndexRefreshes   int            `json:"repl_file_index_refreshes"`
	EfficientPathExpected    int            `json:"efficient_path_expected_scenarios"`
	EfficientPathMatched     int            `json:"efficient_path_matched_scenarios"`
	EfficientPathMisses      int            `json:"efficient_path_missed_scenarios"`
	HybridPolicyObserved     int            `json:"hybrid_policy_observed_scenarios"`
	HybridModeMatched        int            `json:"hybrid_mode_matched_scenarios"`
	HybridModeMismatches     int            `json:"hybrid_mode_mismatch_scenarios"`
	HybridEligible           int            `json:"hybrid_eligible_scenarios"`
	ReplExposed              int            `json:"repl_exposed_scenarios"`
	HybridExposureMatched    int            `json:"hybrid_exposure_matched_scenarios"`
	HybridExposureGaps       int            `json:"hybrid_exposure_gap_scenarios"`
	HybridUnexpectedExposure int            `json:"hybrid_unexpected_exposure_scenarios"`
	HybridStrategies         map[string]int `json:"hybrid_strategies,omitempty"`
	EstimatedUSD             float64        `json:"estimated_usd"`
	CostTracked              int            `json:"cost_tracked_scenarios"`
}

// EngineComparison compares one engine mode with the tools-only baseline.
// All includes every valid pair; Candidates and Controls split pairs by the
// manifest's hybrid_candidate classification.
type EngineComparison struct {
	BaselineMode string                 `json:"baseline_mode"`
	Mode         string                 `json:"mode"`
	All          EngineCohortComparison `json:"all"`
	Candidates   EngineCohortComparison `json:"candidates"`
	Controls     EngineCohortComparison `json:"controls"`
	Provenance   EnginePairProvenance   `json:"provenance"`
	Excluded     EnginePairExclusions   `json:"excluded,omitempty"`
}

// EnginePairProvenance positively proves that paired rows were produced from
// the same scenario contract and complete run specification. Missing hashes
// remain reportable for legacy diagnostics but cannot pass an engine gate.
type EnginePairProvenance struct {
	Pairs                  int `json:"pairs"`
	ScenarioSpecVerified   int `json:"scenario_spec_verified"`
	RunSpecVerified        int `json:"run_spec_verified"`
	ClassificationVerified int `json:"classification_verified"`
}

// EngineCohortComparison reports current-minus-baseline deltas over exactly
// paired rows. Negative efficiency deltas mean the current mode used less.
type EngineCohortComparison struct {
	Pairs               int                        `json:"pairs"`
	BaselinePassed      int                        `json:"baseline_passed"`
	CurrentPassed       int                        `json:"current_passed"`
	PassedDelta         int                        `json:"passed_delta"`
	BaselineScore       ScoreSummary               `json:"baseline_score"`
	CurrentScore        ScoreSummary               `json:"current_score"`
	ScoreDelta          float64                    `json:"score_delta"`
	Hybrid              PairedHybridFunnel         `json:"hybrid"`
	Efficiency          PairedEfficiencyComparison `json:"efficiency"`
	QualityRegressions  []ScenarioIdentity         `json:"quality_regressions,omitempty"`
	QualityImprovements []ScenarioIdentity         `json:"quality_improvements,omitempty"`
}

// PairedHybridFunnel describes the CURRENT engine side of one tools/current
// cohort. Counts share the cohort's exact pairing denominator, so candidate
// adoption cannot be inflated by unmatched runs or a different provider/trial.
type PairedHybridFunnel struct {
	TrustedRuntime     int            `json:"trusted_runtime_pairs"`
	PolicyObserved     int            `json:"policy_observed_pairs"`
	ModeMatched        int            `json:"mode_matched_pairs"`
	ModeMismatches     int            `json:"mode_mismatch_pairs"`
	ExposureMatched    int            `json:"exposure_matched_pairs"`
	ExposureGaps       int            `json:"exposure_gap_pairs"`
	UnexpectedExposure int            `json:"unexpected_exposure_pairs"`
	Eligible           int            `json:"eligible_pairs"`
	Exposed            int            `json:"exposed_pairs"`
	Used               int            `json:"used_pairs"`
	Calls              int            `json:"repl_calls"`
	ScanOperations     int            `json:"repl_scan_operations"`
	FileIndexRefreshes int            `json:"repl_file_index_refreshes"`
	UseRatio           float64        `json:"use_ratio"`
	EfficientExpected  int            `json:"efficient_path_expected_pairs"`
	EfficientMatched   int            `json:"efficient_path_matched_pairs"`
	EfficientMisses    int            `json:"efficient_path_missed_pairs"`
	Strategies         map[string]int `json:"strategies,omitempty"`
}

// PairedEfficiencyComparison retains both sides of every efficiency measure,
// not just a delta. This makes the magnitude interpretable and the lower/equal/
// higher counts expose whether an average is consistent across repeated runs.
type PairedEfficiencyComparison struct {
	InputTokens          PairedMetricComparison `json:"input_tokens"`
	UncachedInputTokens  PairedMetricComparison `json:"uncached_input_tokens"`
	CacheReadInputTokens PairedMetricComparison `json:"cache_read_input_tokens"`
	OutputTokens         PairedMetricComparison `json:"output_tokens"`
	TotalTokens          PairedMetricComparison `json:"total_tokens"`
	ModelRounds          PairedMetricComparison `json:"model_rounds"`
	DurationMillis       PairedMetricComparison `json:"duration_ms"`
	ReplCalls            PairedMetricComparison `json:"repl_calls"`
	EstimatedUSD         PairedMetricComparison `json:"estimated_usd"`
}

type PairedMetricComparison struct {
	Pairs                          int      `json:"pairs"`
	BaselineAverage                float64  `json:"baseline_avg"`
	CurrentAverage                 float64  `json:"current_avg"`
	AverageDelta                   float64  `json:"avg_delta"`
	MedianDelta                    float64  `json:"median_delta"`
	RelativeDelta                  *float64 `json:"relative_delta,omitempty"`
	RelativePairs                  int      `json:"relative_pairs"`
	MedianRelativeDelta            *float64 `json:"median_relative_delta,omitempty"`
	Lower                          int      `json:"lower"`
	Equal                          int      `json:"equal"`
	Higher                         int      `json:"higher"`
	EvidenceUnits                  int      `json:"evidence_units"`
	ClusteredMedianDelta           float64  `json:"clustered_median_delta"`
	ClusteredRelativeEvidenceUnits int      `json:"clustered_relative_evidence_units"`
	ClusteredMedianRelativeDelta   *float64 `json:"clustered_median_relative_delta,omitempty"`
	UnitLower                      int      `json:"unit_lower"`
	UnitEqual                      int      `json:"unit_equal"`
	UnitHigher                     int      `json:"unit_higher"`
	LowerSignTestPValue            *float64 `json:"lower_sign_test_p_value,omitempty"`
}

// EnginePairExclusions explains why rows present in an engine matrix were not
// considered comparable. This prevents a partial matrix from looking like a
// complete A/B result.
type EnginePairExclusions struct {
	BaselineOnly             int `json:"baseline_only,omitempty"`
	CurrentOnly              int `json:"current_only,omitempty"`
	DuplicateCohorts         int `json:"duplicate_cohorts,omitempty"`
	NonExecuted              int `json:"non_executed,omitempty"`
	SpecMismatches           int `json:"spec_mismatches,omitempty"`
	RunSpecMismatches        int `json:"run_spec_mismatches,omitempty"`
	ClassificationMismatches int `json:"classification_mismatches,omitempty"`
}

func (excluded EnginePairExclusions) Total() int {
	return excluded.BaselineOnly + excluded.CurrentOnly + excluded.DuplicateCohorts +
		excluded.NonExecuted + excluded.SpecMismatches + excluded.RunSpecMismatches +
		excluded.ClassificationMismatches
}

// ToolUsageSummary counts the scenarios in which one tool was used at least
// once. It deliberately counts SCENARIOS rather than calls: the question is
// whether a tool gets chosen, not how chatty it is once chosen.
type ToolUsageSummary struct {
	Name      string  `json:"name"`
	Scenarios int     `json:"scenarios"`
	Ratio     float64 `json:"ratio"`
}

// MetricSummary aggregates a boolean metric across scenarios.
type MetricSummary struct {
	Name   string  `json:"name"`
	Passed int     `json:"passed"`
	Total  int     `json:"total"`
	Ratio  float64 `json:"ratio"`
}

// ScenarioSummary is the per-scenario row used in reports.
type ScenarioSummary struct {
	ID                     string         `json:"id"`
	Variant                string         `json:"variant,omitempty"`
	ScenarioSpecHash       string         `json:"scenario_spec_hash,omitempty"`
	HybridCandidate        *bool          `json:"hybrid_candidate,omitempty"`
	Trial                  int            `json:"trial,omitempty"`
	TrialCount             int            `json:"trial_count,omitempty"`
	Status                 string         `json:"status"`
	Score                  ScoreSummary   `json:"score"`
	Error                  string         `json:"error,omitempty"`
	Duration               int64          `json:"duration_ms"`
	TrustedRuntime         bool           `json:"trusted_runtime,omitempty"`
	AgentDuration          int64          `json:"agent_duration_ms,omitempty"`
	InputTokens            int            `json:"input_tokens,omitempty"`
	UncachedInputTokens    int            `json:"uncached_input_tokens,omitempty"`
	CacheReadInputTokens   int            `json:"cache_read_input_tokens,omitempty"`
	OutputTokens           int            `json:"output_tokens,omitempty"`
	TotalTokens            int            `json:"total_tokens,omitempty"`
	TokenBreakdownTracked  bool           `json:"token_breakdown_tracked,omitempty"`
	ModelRounds            int            `json:"model_rounds,omitempty"`
	ReplCalls              int            `json:"repl_calls,omitempty"`
	ReplOperations         map[string]int `json:"repl_operations,omitempty"`
	ReplScanOperations     int            `json:"repl_scan_operations,omitempty"`
	ReplFileIndexRefreshes int            `json:"repl_file_index_refreshes,omitempty"`
	HybridEligible         bool           `json:"hybrid_eligible,omitempty"`
	ReplExposed            bool           `json:"repl_exposed,omitempty"`
	HybridStrategy         string         `json:"hybrid_strategy,omitempty"`
}

// Comparison summarizes current results against a baseline.
type Comparison struct {
	BaselinePath    string                     `json:"baseline_path,omitempty"`
	CurrentPath     string                     `json:"current_path,omitempty"`
	ScoreDelta      float64                    `json:"score_delta"`
	PassedDelta     int                        `json:"passed_delta"`
	Metrics         []MetricDelta              `json:"metrics"`
	Scenarios       []ScenarioDiff             `json:"scenarios"`
	CohortMismatch  *CohortMismatch            `json:"cohort_mismatch,omitempty"`
	InvalidEvidence *ComparisonInvalidEvidence `json:"invalid_evidence,omitempty"`
}

// CohortMismatch records scenario/variant identities that prevent a valid
// aggregate comparison. Aggregate deltas are intentionally left at zero when
// this field is present; the reports did not measure the same cohort.
type CohortMismatch struct {
	BaselineOnly       []ScenarioIdentity `json:"baseline_only,omitempty"`
	CurrentOnly        []ScenarioIdentity `json:"current_only,omitempty"`
	BaselineDuplicates []ScenarioIdentity `json:"baseline_duplicates,omitempty"`
	CurrentDuplicates  []ScenarioIdentity `json:"current_duplicates,omitempty"`
	SpecMismatches     []ScenarioIdentity `json:"spec_mismatches,omitempty"`
}

// ComparisonInvalidEvidence identifies result sets that look structurally
// alike but do not contain executed measurements on both sides.
type ComparisonInvalidEvidence struct {
	BaselineEmpty       bool `json:"baseline_empty,omitempty"`
	CurrentEmpty        bool `json:"current_empty,omitempty"`
	BaselineDryRun      int  `json:"baseline_dry_run,omitempty"`
	CurrentDryRun       int  `json:"current_dry_run,omitempty"`
	BaselineNotExecuted int  `json:"baseline_not_executed,omitempty"`
	CurrentNotExecuted  int  `json:"current_not_executed,omitempty"`
}

// ScenarioIdentity identifies one independently comparable eval result.
type ScenarioIdentity struct {
	ID      string `json:"id"`
	Variant string `json:"variant,omitempty"`
}

// MetricDelta compares one metric pass rate.
type MetricDelta struct {
	Name          string  `json:"name"`
	BaselineRatio float64 `json:"baseline_ratio"`
	CurrentRatio  float64 `json:"current_ratio"`
	Delta         float64 `json:"delta"`
}

// ScenarioDiff compares one scenario/variant status.
type ScenarioDiff struct {
	ID             string  `json:"id"`
	Variant        string  `json:"variant,omitempty"`
	BaselineStatus string  `json:"baseline_status,omitempty"`
	CurrentStatus  string  `json:"current_status,omitempty"`
	ScoreDelta     float64 `json:"score_delta"`
}

// GateOptions describes pass/fail thresholds for an eval report.
type GateOptions struct {
	MinScoreRatio                 float64
	RequireAllPassed              bool
	MaxRegression                 float64
	RequireComparableBaseline     bool
	MetricMinRatios               map[string]float64
	FailOnMissingMetric           bool
	EngineModes                   []string
	RequireCompleteEnginePairs    bool
	MaxEngineScoreRegression      *float64
	MaxEngineQualityRegressions   *int
	EngineMaxRelativeDeltas       map[string]float64
	EngineMaxMedianRelativeDeltas map[string]float64
	EngineMinLowerRatios          map[string]float64
	EngineMaxLowerPValues         map[string]float64
	EngineMinReplUseRatios        map[string]float64
	EngineMaxReplUseRatios        map[string]float64
}

// GateResult is the machine-readable outcome of applying thresholds.
type GateResult struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// BuildReport aggregates eval results into metric and scenario summaries.
func BuildReport(path string, results []Result) Report {
	report := Report{ResultsPath: path, Count: len(results)}
	metricCounts := map[string]*MetricSummary{}
	toolScenarios := map[string]int{}
	engineSummaries := map[string]*EngineSummary{}
	measuredScenarios := 0

	for _, result := range results {
		engineMode := strings.ToLower(strings.TrimSpace(result.EngineMode))
		if engineMode == "" {
			engineMode = "unspecified"
		}
		engineSummary := engineSummaries[engineMode]
		if engineSummary == nil {
			engineSummary = &EngineSummary{Mode: engineMode}
			engineSummaries[engineMode] = engineSummary
		}
		engineSummary.Count++
		scenarioScore := result.Score
		measured := false
		switch result.Status {
		case "passed":
			report.Passed++
			engineSummary.Passed++
			measured = true
		case "dry_run":
			report.DryRun++
			scenarioScore = ScoreSummary{}
		case "failed":
			report.Failed++
			engineSummary.Failed++
			measured = true
		default:
			report.Failed++
			engineSummary.Failed++
			scenarioScore = ScoreSummary{}
		}

		// Only passed/failed results reached actual agent + verification execution.
		// Older or malformed JSONL may attach synthetic successes to dry-run,
		// setup_failed, fixture_missing, or agent_command_missing rows; none is
		// valid score evidence.
		journal := trustedJournal(result)
		if measured {
			measuredScenarios++
			report.Score.Passed += result.Score.Passed
			report.Score.Total += result.Score.Total
			engineSummary.Score.Passed += result.Score.Passed
			engineSummary.Score.Total += result.Score.Total
			if journal != nil {
				engineSummary.Efficiency.TrustedRuntimeScenarios++
				seen := map[string]struct{}{}
				for _, tool := range journal.Tools {
					tool = strings.TrimSpace(tool)
					if tool == "" {
						continue
					}
					if _, dup := seen[tool]; dup {
						continue
					}
					seen[tool] = struct{}{}
					toolScenarios[tool]++
				}
			}

			for name, ok := range result.Metrics {
				summary := metricCounts[name]
				if summary == nil {
					summary = &MetricSummary{Name: name}
					metricCounts[name] = summary
				}
				summary.Total++
				if ok {
					summary.Passed++
				}
			}
			if efficient, expected := result.Metrics["hybrid_efficient_path"]; expected {
				engineSummary.Efficiency.EfficientPathExpected++
				if efficient {
					engineSummary.Efficiency.EfficientPathMatched++
				} else {
					engineSummary.Efficiency.EfficientPathMisses++
				}
			}
		}

		var inputTokens, uncachedInputTokens, cacheReadInputTokens, outputTokens, totalTokens, modelRounds, replCalls, replScanOperations, replFileIndexRefreshes int
		var tokenBreakdownTracked bool
		var replOperations map[string]int
		var hybridEligible, replExposed bool
		var hybridStrategy string
		var agentDuration int64
		if journal != nil {
			replCalls = journal.ToolCounts["repl_exec"]
			replFileIndexRefreshes = journal.ReplFileIndexRefreshes
			if measured {
				engineSummary.Efficiency.ReplFileIndexRefreshes += replFileIndexRefreshes
			}
			if len(journal.ReplOperations) > 0 {
				replScanOperations = replScanOperationCount(journal.ReplOperations)
				replOperations = make(map[string]int, len(journal.ReplOperations))
				for operation, count := range journal.ReplOperations {
					replOperations[operation] = count
					if measured {
						if engineSummary.Efficiency.ReplOperations == nil {
							engineSummary.Efficiency.ReplOperations = make(map[string]int)
						}
						engineSummary.Efficiency.ReplOperations[operation] += count
					}
				}
				if measured {
					engineSummary.Efficiency.ReplScanOperations += replScanOperations
				}
			}
			if policy := journal.HybridPolicy; policy != nil {
				hybridEligible = policy.REPLEligible
				replExposed = policy.REPLEnabled
				hybridStrategy = strings.TrimSpace(policy.Strategy)
				if measured {
					engineSummary.Efficiency.HybridPolicyObserved++
					if hybridStrategy != "" {
						if engineSummary.Efficiency.HybridStrategies == nil {
							engineSummary.Efficiency.HybridStrategies = make(map[string]int)
						}
						engineSummary.Efficiency.HybridStrategies[hybridStrategy]++
					}
					if strings.EqualFold(strings.TrimSpace(policy.Mode), engineMode) {
						engineSummary.Efficiency.HybridModeMatched++
					} else {
						engineSummary.Efficiency.HybridModeMismatches++
					}
					switch {
					case hybridEligible == replExposed:
						engineSummary.Efficiency.HybridExposureMatched++
					case hybridEligible:
						engineSummary.Efficiency.HybridExposureGaps++
					default:
						engineSummary.Efficiency.HybridUnexpectedExposure++
					}
				}
				if measured && hybridEligible {
					engineSummary.Efficiency.HybridEligible++
				}
				if measured && replExposed {
					engineSummary.Efficiency.ReplExposed++
				}
			}
			// Tool-choice evidence is independent of the final token/cost ledger.
			// A missing headless_metrics event must not erase a real REPL call from
			// the eligibility -> exposure -> adoption funnel.
			if measured {
				engineSummary.Efficiency.ReplCalls += replCalls
				if replCalls > 0 {
					engineSummary.Efficiency.ReplUsedScenarios++
				}
			}
			if metrics := journal.HeadlessMetrics; metrics != nil {
				inputTokens = metrics.InputTokens
				cacheReadInputTokens = metrics.CacheReadInputTokens
				uncachedInputTokens = metrics.InputTokens - metrics.CacheReadInputTokens
				outputTokens = metrics.OutputTokens
				totalTokens = metrics.TotalTokens
				modelRounds = metrics.ModelRounds
				agentDuration = metrics.DurationMillis
				if measured {
					engineSummary.Efficiency.MeasuredScenarios++
					engineSummary.Efficiency.TotalTokens += metrics.TotalTokens
					engineSummary.Efficiency.ModelRounds += metrics.ModelRounds
					engineSummary.Efficiency.DurationMillis += metrics.DurationMillis
					engineSummary.Efficiency.EstimatedUSD += metrics.EstimatedUSD
					tokenBreakdownTracked = validTokenBreakdown(metrics)
					if tokenBreakdownTracked {
						engineSummary.Efficiency.TokenBreakdownScenarios++
						engineSummary.Efficiency.InputTokens += metrics.InputTokens
						engineSummary.Efficiency.UncachedInputTokens += uncachedInputTokens
						engineSummary.Efficiency.CacheReadInputTokens += metrics.CacheReadInputTokens
						engineSummary.Efficiency.OutputTokens += metrics.OutputTokens
					}
					if metrics.CostTracked {
						engineSummary.Efficiency.CostTracked++
					}
				}
			}
		}

		report.Scenarios = append(report.Scenarios, ScenarioSummary{
			ID:                     result.ScenarioID,
			Variant:                resultVariant(result),
			ScenarioSpecHash:       result.ScenarioSpecHash,
			HybridCandidate:        result.HybridCandidate,
			Trial:                  result.Trial,
			TrialCount:             result.TrialCount,
			Status:                 result.Status,
			Score:                  scenarioScore,
			Error:                  result.Error,
			Duration:               result.DurationMillis,
			TrustedRuntime:         journal != nil,
			AgentDuration:          agentDuration,
			InputTokens:            inputTokens,
			UncachedInputTokens:    uncachedInputTokens,
			CacheReadInputTokens:   cacheReadInputTokens,
			OutputTokens:           outputTokens,
			TotalTokens:            totalTokens,
			TokenBreakdownTracked:  tokenBreakdownTracked,
			ModelRounds:            modelRounds,
			ReplCalls:              replCalls,
			ReplOperations:         replOperations,
			ReplScanOperations:     replScanOperations,
			ReplFileIndexRefreshes: replFileIndexRefreshes,
			HybridEligible:         hybridEligible,
			ReplExposed:            replExposed,
			HybridStrategy:         hybridStrategy,
		})
	}
	if report.Score.Total > 0 {
		report.Score.Ratio = float64(report.Score.Passed) / float64(report.Score.Total)
	}

	for _, summary := range metricCounts {
		if summary.Total > 0 {
			summary.Ratio = float64(summary.Passed) / float64(summary.Total)
		}
		report.Metrics = append(report.Metrics, *summary)
	}
	sort.Slice(report.Metrics, func(i, j int) bool {
		if report.Metrics[i].Ratio != report.Metrics[j].Ratio {
			return report.Metrics[i].Ratio < report.Metrics[j].Ratio
		}
		return report.Metrics[i].Name < report.Metrics[j].Name
	})
	for name, count := range toolScenarios {
		summary := ToolUsageSummary{Name: name, Scenarios: count}
		if measuredScenarios > 0 {
			summary.Ratio = float64(count) / float64(measuredScenarios)
		}
		report.ToolUsage = append(report.ToolUsage, summary)
	}
	for _, summary := range engineSummaries {
		if summary.Score.Total > 0 {
			summary.Score.Ratio = float64(summary.Score.Passed) / float64(summary.Score.Total)
		}
		report.Engines = append(report.Engines, *summary)
	}
	sort.Slice(report.Engines, func(i, j int) bool {
		return report.Engines[i].Mode < report.Engines[j].Mode
	})
	sort.Slice(report.ToolUsage, func(i, j int) bool {
		if report.ToolUsage[i].Scenarios != report.ToolUsage[j].Scenarios {
			return report.ToolUsage[i].Scenarios > report.ToolUsage[j].Scenarios
		}
		return report.ToolUsage[i].Name < report.ToolUsage[j].Name
	})
	sort.Slice(report.Scenarios, func(i, j int) bool {
		if report.Scenarios[i].Status != report.Scenarios[j].Status {
			return report.Scenarios[i].Status < report.Scenarios[j].Status
		}
		if report.Scenarios[i].ID != report.Scenarios[j].ID {
			return report.Scenarios[i].ID < report.Scenarios[j].ID
		}
		return report.Scenarios[i].Variant < report.Scenarios[j].Variant
	})
	report.EngineComparisons = buildEngineComparisons(results)
	return report
}

type engineCohortAccumulator struct {
	summary              EngineCohortComparison
	inputTokens          pairedMetricAccumulator
	uncachedInputTokens  pairedMetricAccumulator
	cacheReadInputTokens pairedMetricAccumulator
	outputTokens         pairedMetricAccumulator
	totalTokens          pairedMetricAccumulator
	modelRounds          pairedMetricAccumulator
	durationMillis       pairedMetricAccumulator
	replCalls            pairedMetricAccumulator
	estimatedUSD         pairedMetricAccumulator
}

type pairedMetricAccumulator struct {
	pairs         int
	baselineTotal float64
	currentTotal  float64
	lower         int
	equal         int
	higher        int
	deltas        []float64
	relative      []float64
	units         map[string]pairedUnitAccumulator
}

type pairedUnitAccumulator struct {
	deltaTotal    float64
	relativeTotal float64
	pairs         int
	relativePairs int
}

// buildEngineComparisons deliberately pairs raw results rather than derived
// EngineSummary averages. The pairing key excludes only engine mode, so a
// delta can never silently compare a different provider, model, fault profile,
// or scenario.
func buildEngineComparisons(results []Result) []EngineComparison {
	byMode := make(map[string]map[string][]Result)
	for _, result := range results {
		mode := strings.ToLower(strings.TrimSpace(result.EngineMode))
		if mode == "" {
			continue
		}
		if byMode[mode] == nil {
			byMode[mode] = make(map[string][]Result)
		}
		key := enginePairKey(result)
		byMode[mode][key] = append(byMode[mode][key], result)
	}
	baseline, hasBaseline := byMode["tools"]
	if !hasBaseline {
		return nil
	}

	var comparisons []EngineComparison
	for _, mode := range []string{"auto", "hybrid"} {
		current, ok := byMode[mode]
		if !ok {
			continue
		}
		comparison := EngineComparison{BaselineMode: "tools", Mode: mode}
		var all, candidates, controls engineCohortAccumulator
		keys := make(map[string]struct{}, len(baseline)+len(current))
		for key := range baseline {
			keys[key] = struct{}{}
		}
		for key := range current {
			keys[key] = struct{}{}
		}
		for key := range keys {
			baseRows := baseline[key]
			currentRows := current[key]
			switch {
			case len(baseRows) == 0:
				comparison.Excluded.CurrentOnly++
				continue
			case len(currentRows) == 0:
				comparison.Excluded.BaselineOnly++
				continue
			case len(baseRows) != 1 || len(currentRows) != 1:
				comparison.Excluded.DuplicateCohorts++
				continue
			}

			base, cur := baseRows[0], currentRows[0]
			if !executedResult(base) || !executedResult(cur) {
				comparison.Excluded.NonExecuted++
				continue
			}
			if base.ScenarioSpecHash != "" && cur.ScenarioSpecHash != "" && base.ScenarioSpecHash != cur.ScenarioSpecHash {
				comparison.Excluded.SpecMismatches++
				continue
			}
			if base.RunSpecHash != "" && cur.RunSpecHash != "" && base.RunSpecHash != cur.RunSpecHash {
				comparison.Excluded.RunSpecMismatches++
				continue
			}
			if base.HybridCandidate != nil && cur.HybridCandidate != nil && *base.HybridCandidate != *cur.HybridCandidate {
				comparison.Excluded.ClassificationMismatches++
				continue
			}
			comparison.Provenance.Pairs++
			if validSHA256Hex(base.ScenarioSpecHash) && validSHA256Hex(cur.ScenarioSpecHash) {
				comparison.Provenance.ScenarioSpecVerified++
			}
			if validSHA256Hex(base.RunSpecHash) && validSHA256Hex(cur.RunSpecHash) {
				comparison.Provenance.RunSpecVerified++
			}
			if base.HybridCandidate != nil && cur.HybridCandidate != nil {
				comparison.Provenance.ClassificationVerified++
			}

			identity := ScenarioIdentity{ID: cur.ScenarioID, Variant: enginePairVariant(cur)}
			addEnginePair(&all, base, cur, identity)
			// Both rows must carry the same classification before contributing
			// to the candidate/control split. Legacy unclassified rows remain in
			// All but cannot be guessed into either specialized cohort.
			if base.HybridCandidate != nil && cur.HybridCandidate != nil {
				if *cur.HybridCandidate {
					addEnginePair(&candidates, base, cur, identity)
				} else {
					addEnginePair(&controls, base, cur, identity)
				}
			}
		}
		comparison.All = finalizeEngineCohort(all)
		comparison.Candidates = finalizeEngineCohort(candidates)
		comparison.Controls = finalizeEngineCohort(controls)
		comparisons = append(comparisons, comparison)
	}
	return comparisons
}

func validSHA256Hex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func addEnginePair(acc *engineCohortAccumulator, baseline, current Result, identity ScenarioIdentity) {
	acc.summary.Pairs++
	if baseline.Status == "passed" {
		acc.summary.BaselinePassed++
	}
	if current.Status == "passed" {
		acc.summary.CurrentPassed++
	}
	acc.summary.BaselineScore.Passed += baseline.Score.Passed
	acc.summary.BaselineScore.Total += baseline.Score.Total
	acc.summary.CurrentScore.Passed += current.Score.Passed
	acc.summary.CurrentScore.Total += current.Score.Total
	currentReplCalls := resultReplCalls(current)
	acc.summary.Hybrid.Calls += currentReplCalls
	currentJournal := trustedJournal(current)
	if currentJournal != nil {
		acc.summary.Hybrid.TrustedRuntime++
		acc.summary.Hybrid.ScanOperations += replScanOperationCount(currentJournal.ReplOperations)
		acc.summary.Hybrid.FileIndexRefreshes += currentJournal.ReplFileIndexRefreshes
	}
	if currentReplCalls > 0 {
		acc.summary.Hybrid.Used++
	}
	if efficient, expected := current.Metrics["hybrid_efficient_path"]; expected {
		acc.summary.Hybrid.EfficientExpected++
		if efficient {
			acc.summary.Hybrid.EfficientMatched++
		} else {
			acc.summary.Hybrid.EfficientMisses++
		}
	}
	if currentJournal != nil && currentJournal.HybridPolicy != nil {
		policy := currentJournal.HybridPolicy
		acc.summary.Hybrid.PolicyObserved++
		if strategy := strings.TrimSpace(policy.Strategy); strategy != "" {
			if acc.summary.Hybrid.Strategies == nil {
				acc.summary.Hybrid.Strategies = make(map[string]int)
			}
			acc.summary.Hybrid.Strategies[strategy]++
		}
		if strings.EqualFold(strings.TrimSpace(policy.Mode), strings.TrimSpace(current.EngineMode)) {
			acc.summary.Hybrid.ModeMatched++
		} else {
			acc.summary.Hybrid.ModeMismatches++
		}
		switch {
		case policy.REPLEligible == policy.REPLEnabled:
			acc.summary.Hybrid.ExposureMatched++
		case policy.REPLEligible:
			acc.summary.Hybrid.ExposureGaps++
		default:
			acc.summary.Hybrid.UnexpectedExposure++
		}
		if policy.REPLEligible {
			acc.summary.Hybrid.Eligible++
		}
		if policy.REPLEnabled {
			acc.summary.Hybrid.Exposed++
		}
	}

	baseRatio := scoreRatio(baseline.Score)
	currentRatio := scoreRatio(current.Score)
	switch {
	case baseline.Status == "passed" && current.Status != "passed":
		acc.summary.QualityRegressions = append(acc.summary.QualityRegressions, identity)
	case baseline.Status != "passed" && current.Status == "passed":
		acc.summary.QualityImprovements = append(acc.summary.QualityImprovements, identity)
	case currentRatio < baseRatio:
		acc.summary.QualityRegressions = append(acc.summary.QualityRegressions, identity)
	case currentRatio > baseRatio:
		acc.summary.QualityImprovements = append(acc.summary.QualityImprovements, identity)
	}

	baseMetrics := headlessMetrics(baseline)
	currentMetrics := headlessMetrics(current)
	if baseMetrics == nil || currentMetrics == nil {
		return
	}
	unit := engineEvidenceUnitKey(current)
	if validTokenBreakdown(baseMetrics) && validTokenBreakdown(currentMetrics) {
		acc.inputTokens.add(unit, float64(baseMetrics.InputTokens), float64(currentMetrics.InputTokens))
		acc.uncachedInputTokens.add(unit,
			float64(baseMetrics.InputTokens-baseMetrics.CacheReadInputTokens),
			float64(currentMetrics.InputTokens-currentMetrics.CacheReadInputTokens))
		acc.cacheReadInputTokens.add(unit, float64(baseMetrics.CacheReadInputTokens), float64(currentMetrics.CacheReadInputTokens))
		acc.outputTokens.add(unit, float64(baseMetrics.OutputTokens), float64(currentMetrics.OutputTokens))
	}
	acc.totalTokens.add(unit, float64(baseMetrics.TotalTokens), float64(currentMetrics.TotalTokens))
	acc.modelRounds.add(unit, float64(baseMetrics.ModelRounds), float64(currentMetrics.ModelRounds))
	acc.durationMillis.add(unit, float64(baseMetrics.DurationMillis), float64(currentMetrics.DurationMillis))
	acc.replCalls.add(unit, float64(resultReplCalls(baseline)), float64(currentReplCalls))
	if baseMetrics.CostTracked && currentMetrics.CostTracked {
		acc.estimatedUSD.add(unit, baseMetrics.EstimatedUSD, currentMetrics.EstimatedUSD)
	}
}

func finalizeEngineCohort(acc engineCohortAccumulator) EngineCohortComparison {
	summary := acc.summary
	summary.PassedDelta = summary.CurrentPassed - summary.BaselinePassed
	summary.BaselineScore.Ratio = scoreRatio(summary.BaselineScore)
	summary.CurrentScore.Ratio = scoreRatio(summary.CurrentScore)
	summary.ScoreDelta = summary.CurrentScore.Ratio - summary.BaselineScore.Ratio
	if summary.Pairs > 0 {
		summary.Hybrid.UseRatio = float64(summary.Hybrid.Used) / float64(summary.Pairs)
	}
	summary.Efficiency = PairedEfficiencyComparison{
		InputTokens:          acc.inputTokens.finalize(),
		UncachedInputTokens:  acc.uncachedInputTokens.finalize(),
		CacheReadInputTokens: acc.cacheReadInputTokens.finalize(),
		OutputTokens:         acc.outputTokens.finalize(),
		TotalTokens:          acc.totalTokens.finalize(),
		ModelRounds:          acc.modelRounds.finalize(),
		DurationMillis:       acc.durationMillis.finalize(),
		ReplCalls:            acc.replCalls.finalize(),
		EstimatedUSD:         acc.estimatedUSD.finalize(),
	}
	sortScenarioIdentities(summary.QualityRegressions)
	sortScenarioIdentities(summary.QualityImprovements)
	return summary
}

func (acc *pairedMetricAccumulator) add(unit string, baseline, current float64) {
	acc.pairs++
	acc.baselineTotal += baseline
	acc.currentTotal += current
	delta := current - baseline
	acc.deltas = append(acc.deltas, delta)
	if acc.units == nil {
		acc.units = make(map[string]pairedUnitAccumulator)
	}
	unitSummary := acc.units[unit]
	unitSummary.deltaTotal += delta
	unitSummary.pairs++
	if baseline != 0 {
		relative := delta / baseline
		acc.relative = append(acc.relative, relative)
		unitSummary.relativeTotal += relative
		unitSummary.relativePairs++
	}
	acc.units[unit] = unitSummary
	switch {
	case current < baseline:
		acc.lower++
	case current > baseline:
		acc.higher++
	default:
		acc.equal++
	}
}

func (acc pairedMetricAccumulator) finalize() PairedMetricComparison {
	if acc.pairs == 0 {
		return PairedMetricComparison{}
	}
	n := float64(acc.pairs)
	baselineAverage := acc.baselineTotal / n
	currentAverage := acc.currentTotal / n
	delta := currentAverage - baselineAverage
	result := PairedMetricComparison{
		Pairs: acc.pairs, BaselineAverage: baselineAverage, CurrentAverage: currentAverage,
		AverageDelta: delta, MedianDelta: medianFloat64(acc.deltas),
		RelativePairs: len(acc.relative), Lower: acc.lower, Equal: acc.equal, Higher: acc.higher,
	}
	if baselineAverage != 0 {
		relative := delta / baselineAverage
		result.RelativeDelta = &relative
	}
	if len(acc.relative) > 0 {
		medianRelative := medianFloat64(acc.relative)
		result.MedianRelativeDelta = &medianRelative
	}
	unitDeltas := make([]float64, 0, len(acc.units))
	unitRelativeDeltas := make([]float64, 0, len(acc.units))
	for _, unit := range acc.units {
		result.EvidenceUnits++
		unitDeltas = append(unitDeltas, unit.deltaTotal/float64(unit.pairs))
		if unit.relativePairs == unit.pairs {
			unitRelativeDeltas = append(unitRelativeDeltas, unit.relativeTotal/float64(unit.relativePairs))
		}
		switch {
		case unit.deltaTotal < 0:
			result.UnitLower++
		case unit.deltaTotal > 0:
			result.UnitHigher++
		default:
			result.UnitEqual++
		}
	}
	result.ClusteredMedianDelta = medianFloat64(unitDeltas)
	result.ClusteredRelativeEvidenceUnits = len(unitRelativeDeltas)
	if len(unitRelativeDeltas) > 0 {
		clusteredMedianRelative := medianFloat64(unitRelativeDeltas)
		result.ClusteredMedianRelativeDelta = &clusteredMedianRelative
	}
	if nonTied := result.UnitLower + result.UnitHigher; nonTied > 0 {
		p := exactBinomialUpperTail(nonTied, result.UnitLower)
		result.LowerSignTestPValue = &p
	}
	return result
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 != 0 {
		return ordered[middle]
	}
	return ordered[middle-1]/2 + ordered[middle]/2
}

// exactBinomialUpperTail is P[X >= successes] for X~Binomial(trials, 0.5).
// It is the one-sided exact sign-test p-value used for the hypothesis that the
// current engine is lower than tools. Log-space summation remains stable for
// large eval matrices, and ties are excluded by the caller.
func exactBinomialUpperTail(trials, successes int) float64 {
	if trials < 0 || successes < 0 || successes > trials {
		return math.NaN()
	}
	if successes == 0 {
		return 1
	}
	logTrials, _ := math.Lgamma(float64(trials + 1))
	logSuccesses, _ := math.Lgamma(float64(successes + 1))
	logFailures, _ := math.Lgamma(float64(trials - successes + 1))
	logTerm := logTrials - logSuccesses - logFailures - float64(trials)*math.Ln2
	logSum := logTerm
	for k := successes; k < trials; k++ {
		logTerm += math.Log(float64(trials-k)) - math.Log(float64(k+1))
		logSum = logAddExp(logSum, logTerm)
	}
	p := math.Exp(logSum)
	if p > 1 {
		return 1
	}
	return p
}

func logAddExp(a, b float64) float64 {
	if a < b {
		a, b = b, a
	}
	return a + math.Log1p(math.Exp(b-a))
}

func executedResult(result Result) bool {
	return result.Status == "passed" || result.Status == "failed"
}

func scoreRatio(score ScoreSummary) float64 {
	if score.Total == 0 {
		return 0
	}
	return float64(score.Passed) / float64(score.Total)
}

func headlessMetrics(result Result) *HeadlessMetricsSummary {
	journal := trustedJournal(result)
	if journal == nil {
		return nil
	}
	return journal.HeadlessMetrics
}

func validTokenBreakdown(metrics *HeadlessMetricsSummary) bool {
	return metrics != nil && metrics.TokenBreakdownTracked &&
		metrics.InputTokens >= 0 && metrics.OutputTokens >= 0 &&
		metrics.CacheReadInputTokens >= 0 && metrics.CacheReadInputTokens <= metrics.InputTokens &&
		metrics.TotalTokens == metrics.InputTokens+metrics.OutputTokens
}

func resultReplCalls(result Result) int {
	journal := trustedJournal(result)
	if journal == nil {
		return 0
	}
	return journal.ToolCounts["repl_exec"]
}

func trustedJournal(result Result) *JournalSummary {
	if result.Journal == nil || !result.Journal.TrustedRuntime {
		return nil
	}
	return result.Journal
}

func enginePairKey(result Result) string {
	return strings.Join([]string{
		result.ScenarioID,
		strings.TrimSpace(result.Provider),
		strings.TrimSpace(result.Model),
		strings.TrimSpace(result.FaultProfile),
		strconv.Itoa(result.Trial),
	}, "\x00")
}

// engineEvidenceUnitKey deliberately excludes Trial: repeated trials reduce
// noise within one scenario/provider/model/fault cluster but must not inflate
// the number of independent units used by the sign test.
func engineEvidenceUnitKey(result Result) string {
	return strings.Join([]string{
		result.ScenarioID,
		strings.TrimSpace(result.Provider),
		strings.TrimSpace(result.Model),
		strings.TrimSpace(result.FaultProfile),
	}, "\x00")
}

func enginePairVariant(result Result) string {
	parts := make([]string, 0, 3)
	if provider := strings.TrimSpace(result.Provider); provider != "" {
		parts = append(parts, provider)
	}
	if model := strings.TrimSpace(result.Model); model != "" {
		parts = append(parts, model)
	}
	if fault := strings.TrimSpace(result.FaultProfile); fault != "" {
		parts = append(parts, "fault="+fault)
	}
	if result.Trial > 0 {
		trial := "trial=" + strconv.Itoa(result.Trial)
		if result.TrialCount > 0 {
			trial += "/" + strconv.Itoa(result.TrialCount)
		}
		parts = append(parts, trial)
	}
	return strings.Join(parts, "/")
}

// CompareReports compares current aggregate results against a baseline.
func CompareReports(baseline, current Report) Comparison {
	cmp := Comparison{
		BaselinePath: baseline.ResultsPath,
		CurrentPath:  current.ResultsPath,
	}

	baseScenarios := make(map[string]ScenarioSummary, len(baseline.Scenarios))
	currentScenarios := make(map[string]ScenarioSummary, len(current.Scenarios))
	baseCounts := make(map[string]int, len(baseline.Scenarios))
	currentCounts := make(map[string]int, len(current.Scenarios))
	for _, scenario := range baseline.Scenarios {
		key := scenarioKey(scenario.ID, scenario.Variant)
		baseScenarios[key] = scenario
		baseCounts[key]++
	}
	for _, scenario := range current.Scenarios {
		key := scenarioKey(scenario.ID, scenario.Variant)
		currentScenarios[key] = scenario
		currentCounts[key]++
	}

	mismatch := CohortMismatch{}
	for key := range baseScenarios {
		if _, ok := currentScenarios[key]; !ok {
			id, variant := splitScenarioKey(key)
			mismatch.BaselineOnly = append(mismatch.BaselineOnly, ScenarioIdentity{ID: id, Variant: variant})
		}
	}
	for key := range currentScenarios {
		if _, ok := baseScenarios[key]; !ok {
			id, variant := splitScenarioKey(key)
			mismatch.CurrentOnly = append(mismatch.CurrentOnly, ScenarioIdentity{ID: id, Variant: variant})
		}
	}
	for key, count := range baseCounts {
		if count > 1 {
			id, variant := splitScenarioKey(key)
			mismatch.BaselineDuplicates = append(mismatch.BaselineDuplicates, ScenarioIdentity{ID: id, Variant: variant})
		}
	}
	for key, count := range currentCounts {
		if count > 1 {
			id, variant := splitScenarioKey(key)
			mismatch.CurrentDuplicates = append(mismatch.CurrentDuplicates, ScenarioIdentity{ID: id, Variant: variant})
		}
	}
	for key, base := range baseScenarios {
		cur, ok := currentScenarios[key]
		if !ok || baseCounts[key] != 1 || currentCounts[key] != 1 {
			continue
		}
		baseHash := strings.TrimSpace(base.ScenarioSpecHash)
		currentHash := strings.TrimSpace(cur.ScenarioSpecHash)
		// The field is additive for compatibility with existing baselines. Once
		// both sides provide it, a changed prompt/contract/fixture is a different
		// cohort even when the scenario ID stayed the same.
		if baseHash != "" && currentHash != "" && baseHash != currentHash {
			id, variant := splitScenarioKey(key)
			mismatch.SpecMismatches = append(mismatch.SpecMismatches, ScenarioIdentity{ID: id, Variant: variant})
		}
	}
	sortScenarioIdentities(mismatch.BaselineOnly)
	sortScenarioIdentities(mismatch.CurrentOnly)
	sortScenarioIdentities(mismatch.BaselineDuplicates)
	sortScenarioIdentities(mismatch.CurrentDuplicates)
	sortScenarioIdentities(mismatch.SpecMismatches)
	if len(mismatch.BaselineOnly) > 0 || len(mismatch.CurrentOnly) > 0 ||
		len(mismatch.BaselineDuplicates) > 0 || len(mismatch.CurrentDuplicates) > 0 ||
		len(mismatch.SpecMismatches) > 0 {
		cmp.CohortMismatch = &mismatch
	}
	invalid := ComparisonInvalidEvidence{
		BaselineEmpty: len(baseline.Scenarios) == 0,
		CurrentEmpty:  len(current.Scenarios) == 0,
	}
	invalid.BaselineDryRun, invalid.BaselineNotExecuted = invalidScenarioEvidenceCounts(baseline.Scenarios)
	invalid.CurrentDryRun, invalid.CurrentNotExecuted = invalidScenarioEvidenceCounts(current.Scenarios)
	if invalid.BaselineEmpty || invalid.CurrentEmpty || invalid.BaselineDryRun > 0 || invalid.CurrentDryRun > 0 ||
		invalid.BaselineNotExecuted > 0 || invalid.CurrentNotExecuted > 0 {
		cmp.InvalidEvidence = &invalid
	}
	if cmp.CohortMismatch == nil && cmp.InvalidEvidence == nil {
		cmp.ScoreDelta = current.Score.Ratio - baseline.Score.Ratio
		cmp.PassedDelta = current.Passed - baseline.Passed
	}

	if cmp.CohortMismatch == nil && cmp.InvalidEvidence == nil {
		baseMetrics := make(map[string]MetricSummary, len(baseline.Metrics))
		currentMetrics := make(map[string]MetricSummary, len(current.Metrics))
		names := map[string]bool{}
		for _, metric := range baseline.Metrics {
			baseMetrics[metric.Name] = metric
			names[metric.Name] = true
		}
		for _, metric := range current.Metrics {
			currentMetrics[metric.Name] = metric
			names[metric.Name] = true
		}
		for name := range names {
			base, inBase := baseMetrics[name]
			cur, inCur := currentMetrics[name]
			if !inBase || !inCur {
				// A metric that exists in only one report was not measured on
				// both sides and cannot produce a meaningful delta.
				continue
			}
			cmp.Metrics = append(cmp.Metrics, MetricDelta{
				Name:          name,
				BaselineRatio: base.Ratio,
				CurrentRatio:  cur.Ratio,
				Delta:         cur.Ratio - base.Ratio,
			})
		}
		sort.Slice(cmp.Metrics, func(i, j int) bool {
			if cmp.Metrics[i].Delta != cmp.Metrics[j].Delta {
				return cmp.Metrics[i].Delta < cmp.Metrics[j].Delta
			}
			return cmp.Metrics[i].Name < cmp.Metrics[j].Name
		})
	}

	// Per-scenario deltas are meaningful for the intersection even when the
	// overall cohorts differ. Missing identities are represented above rather
	// than compared against zero-valued placeholder scenarios.
	for key, base := range baseScenarios {
		if cmp.InvalidEvidence != nil {
			break
		}
		cur, ok := currentScenarios[key]
		if !ok || baseCounts[key] != 1 || currentCounts[key] != 1 {
			continue
		}
		if base.ScenarioSpecHash != "" && cur.ScenarioSpecHash != "" && base.ScenarioSpecHash != cur.ScenarioSpecHash {
			continue
		}
		id, variant := splitScenarioKey(key)
		cmp.Scenarios = append(cmp.Scenarios, ScenarioDiff{
			ID:             id,
			Variant:        variant,
			BaselineStatus: base.Status,
			CurrentStatus:  cur.Status,
			ScoreDelta:     cur.Score.Ratio - base.Score.Ratio,
		})
	}
	sort.Slice(cmp.Scenarios, func(i, j int) bool {
		if cmp.Scenarios[i].ScoreDelta != cmp.Scenarios[j].ScoreDelta {
			return cmp.Scenarios[i].ScoreDelta < cmp.Scenarios[j].ScoreDelta
		}
		if cmp.Scenarios[i].ID != cmp.Scenarios[j].ID {
			return cmp.Scenarios[i].ID < cmp.Scenarios[j].ID
		}
		return cmp.Scenarios[i].Variant < cmp.Scenarios[j].Variant
	})
	return cmp
}

// EvaluateGate applies quality thresholds to a report and optional baseline
// comparison. Ratios are 0..1 values, so 0.9 means 90%.
func EvaluateGate(report Report, comparison *Comparison, opts GateOptions) GateResult {
	gate := GateResult{Passed: true}
	fail := func(format string, args ...any) {
		gate.Passed = false
		gate.Failures = append(gate.Failures, fmt.Sprintf(format, args...))
	}

	if opts.RequireAllPassed {
		// A "require all passed" gate over ZERO scenarios must FAIL, not pass
		// vacuously: it almost always means a misconfigured scenario filter /
		// glob that matched nothing, and a green CI on zero work is a false
		// signal.
		if report.Count == 0 {
			fail("no scenarios ran — require-pass expects at least one (check the scenario filter/path)")
		} else {
			if report.DryRun > 0 {
				fail("%d scenario(s) were dry-run — require-pass requires actual execution", report.DryRun)
			}
			if report.Failed > 0 {
				fail("%d scenario(s) failed", report.Failed)
			}
		}
	}
	if opts.MinScoreRatio > 0 && report.Score.Ratio < opts.MinScoreRatio {
		fail("score %.1f%% is below required %.1f%%", report.Score.Ratio*100, opts.MinScoreRatio*100)
	}
	if opts.RequireComparableBaseline {
		if comparison == nil {
			fail("a comparable baseline is required for the regression gate")
		} else if comparison.InvalidEvidence != nil {
			fail(
				"comparison contains invalid evidence: empty baseline=%t/current=%t; dry-run evidence=%d baseline/%d current; not-executed=%d baseline/%d current",
				comparison.InvalidEvidence.BaselineEmpty,
				comparison.InvalidEvidence.CurrentEmpty,
				comparison.InvalidEvidence.BaselineDryRun,
				comparison.InvalidEvidence.CurrentDryRun,
				comparison.InvalidEvidence.BaselineNotExecuted,
				comparison.InvalidEvidence.CurrentNotExecuted,
			)
		} else if comparison.CohortMismatch != nil {
			fail(
				"comparison cohort mismatch: %d baseline-only, %d current-only, %d duplicate baseline, %d duplicate current, and %d changed-spec scenario variant(s)",
				len(comparison.CohortMismatch.BaselineOnly),
				len(comparison.CohortMismatch.CurrentOnly),
				len(comparison.CohortMismatch.BaselineDuplicates),
				len(comparison.CohortMismatch.CurrentDuplicates),
				len(comparison.CohortMismatch.SpecMismatches),
			)
		} else if comparison.ScoreDelta < -opts.MaxRegression {
			fail("score regressed %.1fpp, exceeding allowed %.1fpp", -comparison.ScoreDelta*100, opts.MaxRegression*100)
		}
	}

	for name, minRatio := range opts.MetricMinRatios {
		metric, ok := findMetric(report.Metrics, name)
		if !ok {
			if opts.FailOnMissingMetric {
				fail("metric %q is missing", name)
			}
			continue
		}
		if metric.Ratio < minRatio {
			fail("metric %q %.1f%% is below required %.1f%%", name, metric.Ratio*100, minRatio*100)
		}
	}

	evaluateEngineGates(report, opts, fail)

	sort.Strings(gate.Failures)
	return gate
}

func evaluateEngineGates(report Report, opts GateOptions, fail func(string, ...any)) {
	if !opts.RequireCompleteEnginePairs && opts.MaxEngineScoreRegression == nil && opts.MaxEngineQualityRegressions == nil &&
		len(opts.EngineMaxRelativeDeltas) == 0 && len(opts.EngineMaxMedianRelativeDeltas) == 0 && len(opts.EngineMinLowerRatios) == 0 &&
		len(opts.EngineMaxLowerPValues) == 0 &&
		len(opts.EngineMinReplUseRatios) == 0 && len(opts.EngineMaxReplUseRatios) == 0 {
		return
	}
	modes := opts.EngineModes
	if len(modes) == 0 {
		modes = []string{"auto"}
	}
	if opts.MaxEngineScoreRegression != nil && *opts.MaxEngineScoreRegression < 0 {
		fail("engine score regression limit must be non-negative")
		return
	}
	if opts.MaxEngineQualityRegressions != nil && *opts.MaxEngineQualityRegressions < 0 {
		fail("engine quality regression limit must be non-negative")
		return
	}
	comparisons := make(map[string]EngineComparison, len(report.EngineComparisons))
	for _, comparison := range report.EngineComparisons {
		comparisons[strings.ToLower(strings.TrimSpace(comparison.Mode))] = comparison
	}
	seen := make(map[string]bool, len(modes))
	for _, rawMode := range modes {
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		if mode == "" || seen[mode] {
			continue
		}
		seen[mode] = true
		engine, ok := comparisons[mode]
		if !ok {
			fail("engine gate %q has no paired comparison against tools", mode)
			continue
		}
		if engine.All.Pairs == 0 {
			fail("engine %q has no valid tools/%s pairs", mode, mode)
			continue
		}
		if !completeEnginePairProvenance(mode, engine, fail) {
			continue
		}
		if opts.RequireCompleteEnginePairs {
			excluded := engine.Excluded
			excludedCount := excluded.Total()
			if excludedCount > 0 {
				fail("engine %q pairing excluded %d cohort(s): baseline-only=%d current-only=%d duplicates=%d non-executed=%d spec-mismatch=%d run-spec-mismatch=%d classification-mismatch=%d",
					mode, excludedCount, excluded.BaselineOnly, excluded.CurrentOnly,
					excluded.DuplicateCohorts, excluded.NonExecuted, excluded.SpecMismatches,
					excluded.RunSpecMismatches, excluded.ClassificationMismatches)
			}
			measured := engine.All.Efficiency.TotalTokens.Pairs
			if engine.All.Hybrid.TrustedRuntime != engine.All.Pairs {
				fail("engine %q has trusted runtime journal evidence for %d/%d quality pair(s)",
					mode, engine.All.Hybrid.TrustedRuntime, engine.All.Pairs)
			}
			if measured != engine.All.Pairs {
				fail("engine %q has paired headless metrics for %d/%d quality pair(s)", mode, measured, engine.All.Pairs)
			}
			observed := engine.All.Hybrid.PolicyObserved
			if observed != engine.All.Pairs {
				fail("engine %q has paired hybrid policy evidence for %d/%d quality pair(s)", mode, observed, engine.All.Pairs)
			} else if engine.All.Hybrid.ModeMatched != engine.All.Pairs || engine.All.Hybrid.ModeMismatches != 0 {
				fail("engine %q hybrid policy mode provenance matched for %d/%d quality pair(s): mismatches=%d",
					mode, engine.All.Hybrid.ModeMatched, engine.All.Pairs, engine.All.Hybrid.ModeMismatches)
			} else if engine.All.Hybrid.ExposureMatched != engine.All.Pairs ||
				engine.All.Hybrid.ExposureGaps != 0 || engine.All.Hybrid.UnexpectedExposure != 0 {
				fail("engine %q hybrid eligibility/exposure matched for %d/%d quality pair(s): gaps=%d unexpected=%d",
					mode, engine.All.Hybrid.ExposureMatched, engine.All.Pairs,
					engine.All.Hybrid.ExposureGaps, engine.All.Hybrid.UnexpectedExposure)
			}
		}
		if opts.MaxEngineScoreRegression != nil {
			limit := *opts.MaxEngineScoreRegression
			for _, cohort := range []struct {
				name    string
				summary EngineCohortComparison
			}{
				{name: "all", summary: engine.All},
				{name: "candidates", summary: engine.Candidates},
				{name: "controls", summary: engine.Controls},
			} {
				if cohort.summary.Pairs > 0 && cohort.summary.ScoreDelta < -limit {
					fail("engine %q %s score regressed %.1fpp, exceeding allowed %.1fpp",
						mode, cohort.name, -cohort.summary.ScoreDelta*100, limit*100)
				}
			}
		}
		if opts.MaxEngineQualityRegressions != nil {
			count := len(engine.All.QualityRegressions)
			if count > *opts.MaxEngineQualityRegressions {
				fail("engine %q has %d paired quality regression(s), exceeding allowed %d",
					mode, count, *opts.MaxEngineQualityRegressions)
			}
		}
		evaluateEngineEfficiencyThresholds(mode, engine, opts, fail)
		evaluateEngineReplUseThresholds(mode, engine, opts, fail)
	}
	if len(seen) == 0 {
		fail("engine gate has no non-empty mode to evaluate")
	}
}

func completeEnginePairProvenance(mode string, engine EngineComparison, fail func(string, ...any)) bool {
	provenance := engine.Provenance
	complete := true
	if provenance.Pairs != engine.All.Pairs {
		fail("engine %q has pair provenance for %d/%d paired cohort(s)", mode, provenance.Pairs, engine.All.Pairs)
		complete = false
	}
	if provenance.ScenarioSpecVerified != engine.All.Pairs {
		fail("engine %q has verified scenario specification provenance for %d/%d paired cohort(s)",
			mode, provenance.ScenarioSpecVerified, engine.All.Pairs)
		complete = false
	}
	if provenance.RunSpecVerified != engine.All.Pairs {
		fail("engine %q has verified run specification provenance for %d/%d paired cohort(s)",
			mode, provenance.RunSpecVerified, engine.All.Pairs)
		complete = false
	}
	if provenance.ClassificationVerified != engine.All.Pairs {
		fail("engine %q has verified candidate/control classification for %d/%d paired cohort(s)",
			mode, provenance.ClassificationVerified, engine.All.Pairs)
		complete = false
	}
	return complete
}

func evaluateEngineReplUseThresholds(mode string, engine EngineComparison, opts GateOptions, fail func(string, ...any)) {
	check := func(cohortName string, threshold float64, minimum bool) {
		if math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
			kind := "maximum"
			if minimum {
				kind = "minimum"
			}
			fail("engine %q cohort %q has invalid %s REPL use ratio %v", mode, cohortName, kind, threshold)
			return
		}
		cohort, ok := engineCohort(engine, cohortName)
		if !ok {
			fail("engine %q REPL use threshold has invalid cohort %q", mode, cohortName)
			return
		}
		if cohort.Pairs == 0 {
			fail("engine %q cohort %q has no paired REPL adoption evidence", mode, cohortName)
			return
		}
		if cohort.Hybrid.TrustedRuntime != cohort.Pairs {
			fail("engine %q cohort %q has trusted runtime journal evidence for %d/%d REPL adoption pair(s)",
				mode, cohortName, cohort.Hybrid.TrustedRuntime, cohort.Pairs)
			return
		}
		if cohort.Hybrid.PolicyObserved != cohort.Pairs {
			fail("engine %q cohort %q has paired hybrid policy evidence for %d/%d REPL adoption pair(s)",
				mode, cohortName, cohort.Hybrid.PolicyObserved, cohort.Pairs)
			return
		}
		if cohort.Hybrid.ModeMatched != cohort.Pairs || cohort.Hybrid.ModeMismatches != 0 {
			fail("engine %q cohort %q hybrid policy mode provenance matched for %d/%d REPL adoption pair(s): mismatches=%d",
				mode, cohortName, cohort.Hybrid.ModeMatched, cohort.Pairs, cohort.Hybrid.ModeMismatches)
			return
		}
		if cohort.Hybrid.ExposureMatched != cohort.Pairs || cohort.Hybrid.ExposureGaps != 0 ||
			cohort.Hybrid.UnexpectedExposure != 0 {
			fail("engine %q cohort %q hybrid eligibility/exposure matched for %d/%d REPL adoption pair(s): gaps=%d unexpected=%d",
				mode, cohortName, cohort.Hybrid.ExposureMatched, cohort.Pairs,
				cohort.Hybrid.ExposureGaps, cohort.Hybrid.UnexpectedExposure)
			return
		}
		ratio := float64(cohort.Hybrid.Used) / float64(cohort.Pairs)
		if minimum && threshold-ratio > 1e-12 {
			fail("engine %q cohort %q REPL use ratio %.1f%% is below required %.1f%%",
				mode, cohortName, ratio*100, threshold*100)
		}
		if !minimum && ratio-threshold > 1e-12 {
			fail("engine %q cohort %q REPL use ratio %.1f%% exceeds maximum %.1f%%",
				mode, cohortName, ratio*100, threshold*100)
		}
	}
	for cohort, minimum := range opts.EngineMinReplUseRatios {
		check(strings.ToLower(strings.TrimSpace(cohort)), minimum, true)
	}
	for cohort, maximum := range opts.EngineMaxReplUseRatios {
		check(strings.ToLower(strings.TrimSpace(cohort)), maximum, false)
	}
}

func engineCohort(engine EngineComparison, name string) (EngineCohortComparison, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "all":
		return engine.All, true
	case "candidates":
		return engine.Candidates, true
	case "controls":
		return engine.Controls, true
	default:
		return EngineCohortComparison{}, false
	}
}

func evaluateEngineEfficiencyThresholds(mode string, engine EngineComparison, opts GateOptions, fail func(string, ...any)) {
	for key, maximum := range opts.EngineMaxRelativeDeltas {
		if math.IsNaN(maximum) || math.IsInf(maximum, 0) || maximum < -1 {
			fail("engine %q metric %q has invalid maximum relative delta %v", mode, key, maximum)
			continue
		}
		metric, ok := enginePairedMetric(engine, key)
		if !ok {
			fail("engine %q relative-delta threshold has invalid metric %q", mode, key)
			continue
		}
		if !completeEngineMetricEvidence(mode, key, engine, metric, fail) {
			continue
		}
		if metric.RelativeDelta == nil {
			fail("engine %q metric %q has zero baseline average; relative delta is undefined", mode, key)
			continue
		}
		if *metric.RelativeDelta-maximum > 1e-12 {
			fail("engine %q metric %q relative delta %+.1f%% exceeds maximum %+.1f%%",
				mode, key, *metric.RelativeDelta*100, maximum*100)
		}
	}
	for key, maximum := range opts.EngineMaxMedianRelativeDeltas {
		if math.IsNaN(maximum) || math.IsInf(maximum, 0) || maximum < -1 {
			fail("engine %q metric %q has invalid maximum median relative delta %v", mode, key, maximum)
			continue
		}
		metric, ok := enginePairedMetric(engine, key)
		if !ok {
			fail("engine %q median-relative-delta threshold has invalid metric %q", mode, key)
			continue
		}
		if !completeEngineMetricEvidence(mode, key, engine, metric, fail) {
			continue
		}
		if metric.EvidenceUnits == 0 || metric.ClusteredMedianRelativeDelta == nil ||
			metric.ClusteredRelativeEvidenceUnits != metric.EvidenceUnits {
			fail("engine %q metric %q has relative baselines for %d/%d clustered evidence unit(s); clustered median relative delta is incomplete",
				mode, key, metric.ClusteredRelativeEvidenceUnits, metric.EvidenceUnits)
			continue
		}
		if *metric.ClusteredMedianRelativeDelta-maximum > 1e-12 {
			fail("engine %q metric %q clustered median relative delta %+.1f%% exceeds maximum %+.1f%%",
				mode, key, *metric.ClusteredMedianRelativeDelta*100, maximum*100)
		}
	}
	for key, minimum := range opts.EngineMinLowerRatios {
		if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || minimum > 1 {
			fail("engine %q metric %q has invalid minimum lower ratio %v", mode, key, minimum)
			continue
		}
		metric, ok := enginePairedMetric(engine, key)
		if !ok {
			fail("engine %q lower-ratio threshold has invalid metric %q", mode, key)
			continue
		}
		if !completeEngineMetricEvidence(mode, key, engine, metric, fail) {
			continue
		}
		if !completeEngineDirectionEvidence(mode, key, metric, fail) {
			continue
		}
		ratio := float64(metric.UnitLower) / float64(metric.EvidenceUnits)
		if minimum-ratio > 1e-12 {
			fail("engine %q metric %q clustered lower ratio %.1f%% is below required %.1f%%",
				mode, key, ratio*100, minimum*100)
		}
	}
	for key, maximum := range opts.EngineMaxLowerPValues {
		if math.IsNaN(maximum) || math.IsInf(maximum, 0) || maximum < 0 || maximum > 1 {
			fail("engine %q metric %q has invalid maximum lower sign-test p-value %v", mode, key, maximum)
			continue
		}
		metric, ok := enginePairedMetric(engine, key)
		if !ok {
			fail("engine %q lower sign-test threshold has invalid metric %q", mode, key)
			continue
		}
		if !completeEngineMetricEvidence(mode, key, engine, metric, fail) {
			continue
		}
		if !completeEngineDirectionEvidence(mode, key, metric, fail) {
			continue
		}
		if metric.UnitLower+metric.UnitHigher == 0 || metric.LowerSignTestPValue == nil {
			fail("engine %q metric %q has no non-tied evidence units for lower sign test", mode, key)
			continue
		}
		if *metric.LowerSignTestPValue-maximum > 1e-12 {
			fail("engine %q metric %q lower sign-test p-value %.4g exceeds maximum %.4g (units lower/equal/higher %d/%d/%d)",
				mode, key, *metric.LowerSignTestPValue, maximum,
				metric.UnitLower, metric.UnitEqual, metric.UnitHigher)
		}
	}
}

func completeEngineDirectionEvidence(mode, key string, metric PairedMetricComparison, fail func(string, ...any)) bool {
	directions := metric.UnitLower + metric.UnitEqual + metric.UnitHigher
	if metric.EvidenceUnits == 0 || directions != metric.EvidenceUnits {
		fail("engine %q metric %q has incomplete clustered direction evidence (%d/%d unit direction(s))",
			mode, key, directions, metric.EvidenceUnits)
		return false
	}
	return true
}

func completeEngineMetricEvidence(mode, key string, engine EngineComparison, metric PairedMetricComparison, fail func(string, ...any)) bool {
	cohortName, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(key)), ".")
	if !ok {
		fail("engine %q metric %q has invalid cohort provenance", mode, key)
		return false
	}
	cohort, ok := engineCohort(engine, cohortName)
	if !ok {
		fail("engine %q metric %q has invalid cohort provenance", mode, key)
		return false
	}
	if cohort.Pairs == 0 || metric.Pairs == 0 {
		fail("engine %q metric %q has no paired efficiency evidence", mode, key)
		return false
	}
	excluded := engine.Excluded
	excludedCount := excluded.Total()
	if excludedCount > 0 {
		fail("engine %q metric %q cannot gate incomplete evidence with %d excluded cohort(s)", mode, key, excludedCount)
		return false
	}
	if cohortName != "all" && engine.Candidates.Pairs+engine.Controls.Pairs != engine.All.Pairs {
		fail("engine %q metric %q cannot gate incomplete candidate/control classification (%d/%d classified pairs)",
			mode, key, engine.Candidates.Pairs+engine.Controls.Pairs, engine.All.Pairs)
		return false
	}
	if metric.Pairs != cohort.Pairs {
		fail("engine %q metric %q has paired measurements for %d/%d cohort pair(s)",
			mode, key, metric.Pairs, cohort.Pairs)
		return false
	}
	return true
}

func enginePairedMetric(engine EngineComparison, key string) (PairedMetricComparison, bool) {
	cohortName, metricName, ok := strings.Cut(strings.ToLower(strings.TrimSpace(key)), ".")
	if !ok || cohortName == "" || metricName == "" || strings.Contains(metricName, ".") {
		return PairedMetricComparison{}, false
	}
	var cohort EngineCohortComparison
	switch cohortName {
	case "all":
		cohort = engine.All
	case "candidates":
		cohort = engine.Candidates
	case "controls":
		cohort = engine.Controls
	default:
		return PairedMetricComparison{}, false
	}
	switch metricName {
	case "input_tokens":
		return cohort.Efficiency.InputTokens, true
	case "uncached_input_tokens":
		return cohort.Efficiency.UncachedInputTokens, true
	case "cache_read_input_tokens":
		return cohort.Efficiency.CacheReadInputTokens, true
	case "output_tokens":
		return cohort.Efficiency.OutputTokens, true
	case "total_tokens":
		return cohort.Efficiency.TotalTokens, true
	case "model_rounds":
		return cohort.Efficiency.ModelRounds, true
	case "duration_ms":
		return cohort.Efficiency.DurationMillis, true
	case "repl_calls":
		return cohort.Efficiency.ReplCalls, true
	case "estimated_usd":
		return cohort.Efficiency.EstimatedUSD, true
	default:
		return PairedMetricComparison{}, false
	}
}

func sortScenarioIdentities(identities []ScenarioIdentity) {
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].ID != identities[j].ID {
			return identities[i].ID < identities[j].ID
		}
		return identities[i].Variant < identities[j].Variant
	})
}

func invalidScenarioEvidenceCounts(scenarios []ScenarioSummary) (dryRun, notExecuted int) {
	for _, scenario := range scenarios {
		switch scenario.Status {
		case "passed", "failed":
			// These statuses are assigned only after the agent and verification
			// commands ran, so both are legitimate measurements.
		case "dry_run":
			dryRun++
		default:
			notExecuted++
		}
	}
	return dryRun, notExecuted
}

func ParseMetricThresholds(values []string) (map[string]float64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]float64, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		name, raw, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("metric threshold %q must use name=value", value)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("metric threshold %q has empty metric name", value)
		}
		ratio, err := parseRatio(raw)
		if err != nil {
			return nil, fmt.Errorf("metric threshold %q: %w", value, err)
		}
		out[name] = ratio
	}
	return out, nil
}

// ParseEngineMaxRelativeDeltas parses repeated
// "cohort.metric=signed-ratio" thresholds such as
// "candidates.total_tokens=-5%" or "controls.duration_ms=10%".
func ParseEngineMaxRelativeDeltas(values []string) (map[string]float64, error) {
	return parseEngineEfficiencyThresholds(values, parseSignedRatio)
}

// ParseEngineMaxMedianRelativeDeltas parses robust clustered magnitude gates.
// Repeated trials are averaged within scenario/provider/model/fault evidence
// units before their median is taken. Every paired baseline must be non-zero.
func ParseEngineMaxMedianRelativeDeltas(values []string) (map[string]float64, error) {
	return parseEngineEfficiencyThresholds(values, parseSignedRatio)
}

// ParseEngineMinLowerRatios parses the minimum fraction of clustered
// scenario/provider/model/fault evidence units where the current engine's
// average repeated-trial delta must be lower than tools.
func ParseEngineMinLowerRatios(values []string) (map[string]float64, error) {
	return parseEngineEfficiencyThresholds(values, parseRatio)
}

// ParseEngineMaxLowerPValues parses the maximum one-sided exact sign-test
// p-value for scenario/provider/model/fault evidence units. Repeated trials are
// averaged within one unit before their lower/equal/higher direction is tested.
func ParseEngineMaxLowerPValues(values []string) (map[string]float64, error) {
	return parseEngineEfficiencyThresholds(values, parseRatio)
}

// ParseEngineReplUseRatios parses repeated "cohort=ratio" thresholds. The
// ratio is current-engine pairs with at least one repl_exec call divided by all
// valid pairs in that cohort; supported cohorts are all/candidates/controls.
func ParseEngineReplUseRatios(values []string) (map[string]float64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]float64, len(values))
	for _, value := range values {
		cohort, raw, ok := strings.Cut(strings.TrimSpace(value), "=")
		if !ok {
			return nil, fmt.Errorf("engine REPL use threshold %q must use cohort=ratio", value)
		}
		cohort = strings.ToLower(strings.TrimSpace(cohort))
		switch cohort {
		case "all", "candidates", "controls":
		default:
			return nil, fmt.Errorf("engine REPL use threshold %q has invalid cohort %q (expected all, candidates, or controls)", value, cohort)
		}
		ratio, err := parseRatio(raw)
		if err != nil {
			return nil, fmt.Errorf("engine REPL use threshold %q: %w", value, err)
		}
		out[cohort] = ratio
	}
	return out, nil
}

func parseEngineEfficiencyThresholds(values []string, parseValue func(string) (float64, error)) (map[string]float64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]float64, len(values))
	for _, value := range values {
		key, raw, ok := strings.Cut(strings.TrimSpace(value), "=")
		if !ok {
			return nil, fmt.Errorf("engine efficiency threshold %q must use cohort.metric=value", value)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if _, ok := enginePairedMetric(EngineComparison{}, key); !ok {
			return nil, fmt.Errorf("engine efficiency threshold %q has invalid key %q (expected all|candidates|controls and input_tokens|uncached_input_tokens|cache_read_input_tokens|output_tokens|total_tokens|model_rounds|duration_ms|repl_calls|estimated_usd)", value, key)
		}
		threshold, err := parseValue(raw)
		if err != nil {
			return nil, fmt.Errorf("engine efficiency threshold %q: %w", value, err)
		}
		out[key] = threshold
	}
	return out, nil
}

func ParseRatio(value string) (float64, error) {
	return parseRatio(value)
}

func resultVariant(result Result) string {
	base := ""
	switch {
	case result.Provider != "" && result.Model != "":
		base = result.Provider + "/" + result.Model
	case result.Provider != "":
		base = result.Provider
	case result.Model != "":
		base = result.Model
	}
	if result.EngineMode != "" {
		if base != "" {
			base += "/"
		}
		base += "engine=" + result.EngineMode
	}
	if result.FaultProfile != "" {
		if base != "" {
			base += "/"
		}
		base += "fault=" + result.FaultProfile
	}
	if result.Trial > 0 {
		if base != "" {
			base += "/"
		}
		base += "trial=" + strconv.Itoa(result.Trial)
		if result.TrialCount > 0 {
			base += "/" + strconv.Itoa(result.TrialCount)
		}
	}
	return base
}

func scenarioKey(id, variant string) string {
	return id + "\x00" + variant
}

func splitScenarioKey(key string) (string, string) {
	for i := range key {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

func findMetric(metrics []MetricSummary, name string) (MetricSummary, bool) {
	for _, metric := range metrics {
		if metric.Name == name {
			return metric, true
		}
	}
	return MetricSummary{}, false
}

func parseRatio(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty ratio")
	}
	percent := strings.HasSuffix(value, "%")
	value = strings.TrimSuffix(value, "%")
	ratio, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ratio %q", value)
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0, fmt.Errorf("ratio must be a finite number")
	}
	if percent || ratio > 1 {
		ratio /= 100
	}
	if ratio < 0 || ratio > 1 {
		return 0, fmt.Errorf("ratio must be between 0 and 1 or 0%% and 100%%")
	}
	return ratio, nil
}

func parseSignedRatio(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty ratio")
	}
	percent := strings.HasSuffix(value, "%")
	value = strings.TrimSpace(strings.TrimSuffix(value, "%"))
	ratio, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ratio %q", value)
	}
	if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0, fmt.Errorf("ratio must be a finite number")
	}
	if percent || math.Abs(ratio) > 1 {
		ratio /= 100
	}
	if ratio < -1 {
		return 0, fmt.Errorf("relative reduction cannot be below -100%%")
	}
	return ratio, nil
}
