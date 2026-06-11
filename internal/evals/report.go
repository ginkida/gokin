package evals

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Report summarizes one eval results JSONL file.
type Report struct {
	ResultsPath string            `json:"results_path,omitempty"`
	Count       int               `json:"count"`
	Passed      int               `json:"passed"`
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
	ID       string       `json:"id"`
	Variant  string       `json:"variant,omitempty"`
	Status   string       `json:"status"`
	Score    ScoreSummary `json:"score"`
	Error    string       `json:"error,omitempty"`
	Duration int64        `json:"duration_ms"`
}

// Comparison summarizes current results against a baseline.
type Comparison struct {
	BaselinePath string         `json:"baseline_path,omitempty"`
	CurrentPath  string         `json:"current_path,omitempty"`
	ScoreDelta   float64        `json:"score_delta"`
	PassedDelta  int            `json:"passed_delta"`
	Metrics      []MetricDelta  `json:"metrics"`
	Scenarios    []ScenarioDiff `json:"scenarios"`
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
	MinScoreRatio       float64
	RequireAllPassed    bool
	MaxRegression       float64
	MetricMinRatios     map[string]float64
	FailOnMissingMetric bool
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
		if result.Status == "passed" || result.Status == "dry_run" {
			report.Passed++
		}
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

		report.Scenarios = append(report.Scenarios, ScenarioSummary{
			ID:       result.ScenarioID,
			Variant:  resultVariant(result),
			Status:   result.Status,
			Score:    result.Score,
			Error:    result.Error,
			Duration: result.DurationMillis,
		})
	}
	report.Failed = report.Count - report.Passed
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
		ScoreDelta:   current.Score.Ratio - baseline.Score.Ratio,
		PassedDelta:  current.Passed - baseline.Passed,
	}

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
		base := baseMetrics[name]
		cur := currentMetrics[name]
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

	baseScenarios := make(map[string]ScenarioSummary, len(baseline.Scenarios))
	currentScenarios := make(map[string]ScenarioSummary, len(current.Scenarios))
	scenarioKeys := map[string]bool{}
	for _, scenario := range baseline.Scenarios {
		key := scenarioKey(scenario.ID, scenario.Variant)
		baseScenarios[key] = scenario
		scenarioKeys[key] = true
	}
	for _, scenario := range current.Scenarios {
		key := scenarioKey(scenario.ID, scenario.Variant)
		currentScenarios[key] = scenario
		scenarioKeys[key] = true
	}
	for key := range scenarioKeys {
		base := baseScenarios[key]
		cur := currentScenarios[key]
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

	if opts.RequireAllPassed && report.Failed > 0 {
		fail("%d scenario(s) failed", report.Failed)
	}
	if opts.MinScoreRatio > 0 && report.Score.Ratio < opts.MinScoreRatio {
		fail("score %.1f%% is below required %.1f%%", report.Score.Ratio*100, opts.MinScoreRatio*100)
	}
	if comparison != nil && opts.MaxRegression > 0 && comparison.ScoreDelta < -opts.MaxRegression {
		fail("score regressed %.1fpp, exceeding allowed %.1fpp", -comparison.ScoreDelta*100, opts.MaxRegression*100)
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
	switch {
	case result.Provider != "" && result.Model != "":
		return result.Provider + "/" + result.Model
	case result.Provider != "":
		return result.Provider
	case result.Model != "":
		return result.Model
	default:
		return ""
	}
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
	if percent || ratio > 1 {
		ratio /= 100
	}
	if ratio < 0 || ratio > 1 {
		return 0, fmt.Errorf("ratio must be between 0 and 1 or 0%% and 100%%")
	}
	return ratio, nil
}
