package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A baseline that predates scenarios still compares cleanly — over the subset
// it happens to contain — and the report prints a confident percentage for a
// suite that baseline never ran. Every committed baseline is 23 of 34 today,
// so this is the live case, not a hypothetical: the tool that answers the
// question (`eval baseline-audit`) exists and nothing in the comparison path
// consulted it.
func writeStaleBaselineFixture(t *testing.T) (manifest, baseline, results, addedID string) {
	t.Helper()
	dir := t.TempDir()

	// Built from the real manifest, trimmed to two scenarios: a hand-written
	// minimal one is rejected by the loader, and the skip-on-error path would
	// then hide the very condition under test.
	manifest = filepath.Join(dir, "manifest.json")
	real, err := os.ReadFile(filepath.Join("..", "..", "evals", "coding", "manifest.json"))
	if err != nil {
		t.Skip("manifest unavailable")
	}
	var m map[string]any
	if err := json.Unmarshal(real, &m); err != nil {
		t.Fatal(err)
	}
	all, ok := m["scenarios"].([]any)
	if !ok || len(all) < 2 {
		t.Fatalf("manifest has no scenario list to trim")
	}
	m["scenarios"] = all[:2]
	keptID, _ := all[0].(map[string]any)["id"].(string)
	addedID, _ = all[1].(map[string]any)["id"].(string)
	if keptID == "" || addedID == "" {
		t.Fatal("manifest scenarios have no ids")
	}
	trimmed, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, trimmed, 0644); err != nil {
		t.Fatal(err)
	}

	// Built from a real committed row so the fixture matches the true schema
	// rather than a guess at it — score and agent are structs, not scalars.
	template, err := os.ReadFile(filepath.Join("..", "..", "evals", "coding", "baselines", "deepseek.jsonl"))
	if err != nil {
		t.Skip("committed baseline unavailable")
	}
	first := strings.SplitN(strings.TrimSpace(string(template)), "\n", 2)[0]
	row := func(id string) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(first), &m); err != nil {
			t.Fatal(err)
		}
		m["scenario_id"] = id
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// The baseline knows only the older scenario; the run covers both.
	baseline = filepath.Join(dir, "baseline.jsonl")
	if err := os.WriteFile(baseline, []byte(row(keptID)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	results = filepath.Join(dir, "results.jsonl")
	if err := os.WriteFile(results, []byte(row(keptID)+"\n"+row(addedID)+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return manifest, baseline, results, addedID
}

func TestEvalReport_NamesAScenarioTheBaselineNeverRan(t *testing.T) {
	manifest, baseline, results, addedID := writeStaleBaselineFixture(t)

	cmd := newEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"report", "--input", results, "--baseline", baseline, "--manifest", manifest})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("report: %v\n%s", err, out.String())
	}

	got := out.String()
	if !strings.Contains(got, addedID) {
		t.Fatalf("the comparison hid a scenario the baseline never ran:\n%s", got)
	}
	if !strings.Contains(got, "1/2") {
		t.Fatalf("coverage must be stated as a count, not implied:\n%s", got)
	}
}

// Silence is right when the baseline covers the suite — otherwise every
// comparison would carry a warning and the real ones would stop being read.
func TestEvalReport_CompleteBaselineSaysNothing(t *testing.T) {
	manifest, _, results, _ := writeStaleBaselineFixture(t)

	cmd := newEvalCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// results covers both scenarios, so used as its own baseline it is complete.
	cmd.SetArgs([]string{"report", "--input", results, "--baseline", results, "--manifest", manifest})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("report: %v\n%s", err, out.String())
	}

	if strings.Contains(out.String(), "Baseline coverage") {
		t.Fatalf("a complete baseline must not be reported as stale:\n%s", out.String())
	}
}
