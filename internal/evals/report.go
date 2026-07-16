package evals

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Report summarizes one eval results JSONL file.
type Report struct {
	ResultsPath string            `json:"results_path,omitempty"`
	Count       int               `json:"count"`
	Passed      int               `json:"passed"`
	DryRun      int               `json:"dry_run"`
	Failed      int               `json:"failed"`
	Score       ScoreSummary      `json:"score"`
	Metrics     []MetricSummary   `json:"metrics"`
	Scenarios   []ScenarioSummary `json:"scenarios"`
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
	ID               string       `json:"id"`
	Variant          string       `json:"variant,omitempty"`
	ScenarioSpecHash string       `json:"scenario_spec_hash,omitempty"`
	Status           string       `json:"status"`
	Score            ScoreSummary `json:"score"`
	Error            string       `json:"error,omitempty"`
	Duration         int64        `json:"duration_ms"`
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
	MinScoreRatio             float64
	RequireAllPassed          bool
	MaxRegression             float64
	RequireComparableBaseline bool
	MetricMinRatios           map[string]float64
	FailOnMissingMetric       bool
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

	for _, result := range results {
		scenarioScore := result.Score
		measured := false
		switch result.Status {
		case "passed":
			report.Passed++
			measured = true
		case "dry_run":
			report.DryRun++
			scenarioScore = ScoreSummary{}
		case "failed":
			report.Failed++
			measured = true
		default:
			report.Failed++
			scenarioScore = ScoreSummary{}
		}

		// Only passed/failed results reached actual agent + verification execution.
		// Older or malformed JSONL may attach synthetic successes to dry-run,
		// setup_failed, fixture_missing, or agent_command_missing rows; none is
		// valid score evidence.
		if measured {
			report.Score.Passed += result.Score.Passed
			report.Score.Total += result.Score.Total

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
		}

		report.Scenarios = append(report.Scenarios, ScenarioSummary{
			ID:               result.ScenarioID,
			Variant:          resultVariant(result),
			ScenarioSpecHash: result.ScenarioSpecHash,
			Status:           result.Status,
			Score:            scenarioScore,
			Error:            result.Error,
			Duration:         result.DurationMillis,
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
	sort.Slice(report.Scenarios, func(i, j int) bool {
		if report.Scenarios[i].Status != report.Scenarios[j].Status {
			return report.Scenarios[i].Status < report.Scenarios[j].Status
		}
		if report.Scenarios[i].ID != report.Scenarios[j].ID {
			return report.Scenarios[i].ID < report.Scenarios[j].ID
		}
		return report.Scenarios[i].Variant < report.Scenarios[j].Variant
	})
	return report
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

	sort.Strings(gate.Failures)
	return gate
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
	if result.FaultProfile != "" {
		if base != "" {
			base += "/"
		}
		base += "fault=" + result.FaultProfile
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
