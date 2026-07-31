package evals

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeBaselineAuditManifest(t *testing.T, dir string) string {
	t.Helper()
	manifest := Manifest{
		Version: 1,
		Name:    "audit",
		Metrics: []string{"task_completed"},
		Scenarios: []Scenario{
			{
				ID: "a", Category: "bugfix", Difficulty: "small",
				Prompt: "fix a", Fixture: "a",
				ExpectedBehaviors: []string{"fix"}, VerificationCommands: []string{"true"},
				SuccessCriteria: []string{"passes"}, FailureSignals: []string{"fails"}, MaxToolCalls: 3,
			},
			{
				ID: "b", Category: "bugfix", Difficulty: "small",
				Prompt: "fix b", Fixture: "b",
				ExpectedBehaviors: []string{"fix"}, VerificationCommands: []string{"true"},
				SuccessCriteria: []string{"passes"}, FailureSignals: []string{"fails"}, MaxToolCalls: 3,
			},
		},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeBaselineAuditResults(t *testing.T, path string, results []Result) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, result := range results {
		if err := enc.Encode(result); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditBaselineCoverageDetectsMissingUnknownAndDuplicateRows(t *testing.T) {
	dir := t.TempDir()
	manifest := writeBaselineAuditManifest(t, dir)
	resultsPath := filepath.Join(dir, "baseline.jsonl")
	writeBaselineAuditResults(t, resultsPath, []Result{
		{ScenarioID: "a", Provider: "glm", Model: "m"},
		{ScenarioID: "a", Provider: "glm", Model: "m"},
		{ScenarioID: "old", Provider: "glm", Model: "m"},
	})

	audit, err := AuditBaselineCoverage(manifest, resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	if audit.Complete || len(audit.Variants) != 1 {
		t.Fatalf("audit = %+v, want one incomplete variant", audit)
	}
	got := audit.Variants[0]
	if got.Present != 1 || got.Expected != 2 ||
		len(got.Missing) != 1 || got.Missing[0] != "b" ||
		len(got.Unknown) != 1 || got.Unknown[0] != "old" ||
		len(got.Duplicates) != 1 || got.Duplicates[0] != "a" {
		t.Fatalf("variant coverage = %+v", got)
	}
}

func TestAuditBaselineCoverageAcceptsCompleteMultipleVariants(t *testing.T) {
	dir := t.TempDir()
	manifest := writeBaselineAuditManifest(t, dir)
	resultsPath := filepath.Join(dir, "baseline.jsonl")
	var results []Result
	for _, provider := range []string{"glm", "kimi"} {
		for _, id := range []string{"a", "b"} {
			results = append(results, Result{ScenarioID: id, Provider: provider})
		}
	}
	writeBaselineAuditResults(t, resultsPath, results)

	audit, err := AuditBaselineCoverage(manifest, resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !audit.Complete || len(audit.Variants) != 2 {
		t.Fatalf("audit = %+v, want two complete variants", audit)
	}
}
