package evals

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadResultsRejectsMalformedProvenanceHashes(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Result)
		want   string
	}{
		{name: "run spec", mutate: func(result *Result) { result.RunSpecHash = "same" }, want: "run_spec_hash"},
		{name: "scenario spec", mutate: func(result *Result) { result.ScenarioSpecHash = "same" }, want: "scenario_spec_hash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := Result{ScenarioID: "s", Status: "passed", Score: ScoreSummary{Passed: 1, Total: 1}}
			test.mutate(&result)
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "results.jsonl")
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = ReadResults(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadResults() error = %v, want %q", err, test.want)
			}
		})
	}
}
