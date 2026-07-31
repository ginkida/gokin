package evals

import (
	"fmt"
	"sort"
	"strings"
)

// BaselineVariantCoverage describes whether one provider/model/fault cohort
// contains exactly one result for every scenario in the current manifest.
type BaselineVariantCoverage struct {
	Variant    string   `json:"variant"`
	Present    int      `json:"present"`
	Expected   int      `json:"expected"`
	Missing    []string `json:"missing,omitempty"`
	Unknown    []string `json:"unknown,omitempty"`
	Duplicates []string `json:"duplicates,omitempty"`
	Complete   bool     `json:"complete"`
}

// BaselineCoverage is the machine-checkable cohort audit for one JSONL file.
type BaselineCoverage struct {
	InputPath string                    `json:"input_path"`
	Complete  bool                      `json:"complete"`
	Variants  []BaselineVariantCoverage `json:"variants"`
}

// AuditBaselineCoverage detects stale or malformed baseline cohorts without
// spending provider tokens. Every variant present in the results file must
// cover every current manifest scenario exactly once; unknown and duplicate
// rows fail closed as well.
func AuditBaselineCoverage(manifestPath, inputPath string) (BaselineCoverage, error) {
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		return BaselineCoverage{}, err
	}
	results, err := ReadResults(inputPath)
	if err != nil {
		return BaselineCoverage{}, fmt.Errorf("read baseline results: %w", err)
	}

	expected := make(map[string]struct{}, len(manifest.Scenarios))
	for _, scenario := range manifest.Scenarios {
		expected[scenario.ID] = struct{}{}
	}
	countsByVariant := make(map[string]map[string]int)
	for _, result := range results {
		variant := resultVariant(result)
		if variant == "" {
			variant = "default"
		}
		if countsByVariant[variant] == nil {
			countsByVariant[variant] = make(map[string]int)
		}
		countsByVariant[variant][strings.TrimSpace(result.ScenarioID)]++
	}
	if len(countsByVariant) == 0 {
		countsByVariant["default"] = make(map[string]int)
	}

	audit := BaselineCoverage{InputPath: inputPath, Complete: true}
	variants := make([]string, 0, len(countsByVariant))
	for variant := range countsByVariant {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	for _, variant := range variants {
		counts := countsByVariant[variant]
		coverage := BaselineVariantCoverage{
			Variant:  variant,
			Expected: len(expected),
		}
		for id := range expected {
			if counts[id] == 0 {
				coverage.Missing = append(coverage.Missing, id)
			} else {
				coverage.Present++
			}
		}
		for id, count := range counts {
			if _, ok := expected[id]; !ok {
				coverage.Unknown = append(coverage.Unknown, id)
			}
			if count > 1 {
				coverage.Duplicates = append(coverage.Duplicates, id)
			}
		}
		sort.Strings(coverage.Missing)
		sort.Strings(coverage.Unknown)
		sort.Strings(coverage.Duplicates)
		coverage.Complete = len(coverage.Missing) == 0 &&
			len(coverage.Unknown) == 0 &&
			len(coverage.Duplicates) == 0
		audit.Complete = audit.Complete && coverage.Complete
		audit.Variants = append(audit.Variants, coverage)
	}
	return audit, nil
}
