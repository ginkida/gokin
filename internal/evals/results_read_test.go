package evals

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadResultsAcceptsRecordsAboveScannerDefaultLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	result := Result{
		ScenarioID: "large", Status: "passed", Metrics: map[string]bool{},
		Score:    ScoreSummary{Passed: 1, Total: 1},
		Metadata: map[string]string{"note": strings.Repeat("x", 96<<10)},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= 64<<10 {
		t.Fatalf("test record = %d bytes, want above Scanner's default limit", len(encoded))
	}
	content := append([]byte("\n"), encoded...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := ReadResults(path)
	if err != nil {
		t.Fatalf("ReadResults: %v", err)
	}
	if len(results) != 1 || results[0].Metadata["note"] != result.Metadata["note"] {
		t.Fatalf("decoded large result = %+v", results)
	}
}

func TestReadResultsRejectsAmbiguousRecords(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			name: "duplicate field",
			line: `{"scenario_id":"s","status":"passed","status":"failed","metrics":{},"score":{}}`,
			want: `duplicate JSON key "status"`,
		},
		{
			name: "duplicate nested field",
			line: `{"scenario_id":"s","status":"passed","metrics":{},"score":{"passed":1,"passed":0}}`,
			want: `duplicate JSON key "passed"`,
		},
		{
			name: "unknown field",
			line: `{"scenario_id":"s","status":"passed","metrics":{},"score":{},"trusted_runtime":true}`,
			want: `unknown field "trusted_runtime"`,
		},
		{
			name: "multiple documents",
			line: `{"scenario_id":"s","status":"passed","metrics":{},"score":{}} {}`,
			want: "multiple JSON values",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "results.jsonl")
			if err := os.WriteFile(path, []byte("\n"+test.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadResults(path)
			if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadResults() error = %v, want line 2 and %q", err, test.want)
			}
		})
	}
}

func TestReadResultsRejectsOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	content := []byte(`{"scenario_id":"` + strings.Repeat("x", maxResultLineBytes) + `"}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadResults(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("ReadResults() oversized error = %v", err)
	}
}

func TestReadResultsRejectsInvalidSemantics(t *testing.T) {
	valid := func() Result {
		return Result{
			ScenarioID: "s", EngineMode: "auto", Status: "passed",
			Metrics: map[string]bool{"task_completed": true},
			Score:   ScoreSummary{Passed: 1, Total: 1, Ratio: 1},
		}
	}
	tests := []struct {
		name   string
		mutate func(*Result)
		want   string
	}{
		{"missing scenario", func(result *Result) { result.ScenarioID = "" }, "scenario_id is required"},
		{"unknown status", func(result *Result) { result.Status = "running" }, "invalid status"},
		{"unknown engine", func(result *Result) { result.EngineMode = "python-only" }, "invalid engine_mode"},
		{"impossible score", func(result *Result) { result.Score.Passed = 2 }, "invalid score"},
		{"negative duration", func(result *Result) { result.DurationMillis = -1 }, "duration_ms"},
		{"incomplete trial", func(result *Result) { result.TrialCount = 2 }, "invalid trial provenance"},
		{"trial above count", func(result *Result) { result.Trial, result.TrialCount = 3, 2 }, "invalid trial provenance"},
		{"trusted journal without path", func(result *Result) {
			result.Journal = &JournalSummary{TrustedRuntime: true}
		}, "requires path"},
		{"negative tool count", func(result *Result) {
			result.Journal = &JournalSummary{ToolCounts: map[string]int{"repl_exec": -1}}
		}, "invalid tool count"},
		{"invalid operation name", func(result *Result) {
			result.Journal = &JournalSummary{ReplOperations: map[string]int{"count-code": 1}}
		}, "invalid REPL operation"},
		{"negative headless metric", func(result *Result) {
			result.Journal = &JournalSummary{HeadlessMetrics: &HeadlessMetricsSummary{TotalTokens: -1}}
		}, "headless metric"},
		{"cache read above input", func(result *Result) {
			result.Journal = &JournalSummary{HeadlessMetrics: &HeadlessMetricsSummary{InputTokens: 10, CacheReadInputTokens: 11}}
		}, "must not exceed input_tokens"},
		{"inconsistent tracked token breakdown", func(result *Result) {
			result.Journal = &JournalSummary{HeadlessMetrics: &HeadlessMetricsSummary{
				InputTokens: 10, OutputTokens: 5, TotalTokens: 14, TokenBreakdownTracked: true,
			}}
		}, "total_tokens=input_tokens+output_tokens"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := valid()
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
			if err == nil || !strings.Contains(err.Error(), "validate result line 1") ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("ReadResults() error = %v, want semantic error %q", err, test.want)
			}
		})
	}
}
