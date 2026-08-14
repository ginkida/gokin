package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/repl"
	"gokin/internal/security"
	"gokin/internal/tools"
)

// TestREPLJournalEvidenceSurvivesResultRedaction pins the boundary that made
// the operation counters invisible in a real run: doExecuteTool passes
// result.Data through the executor's secret redactor before OnToolEnd sees it,
// and RedactAny JSON round-trips every struct into map[string]any. A journal
// reader that only understood the typed repl.Result therefore recorded nothing
// while unit tests over a hand-built typed value kept passing.
//
// Route the value through the SAME redactor the executor constructs, so this
// test fails if the runtime shape changes again.
func TestREPLJournalEvidenceSurvivesResultRedaction(t *testing.T) {
	source := repl.Result{
		Generation:         3,
		Operations:         map[string]int{"count_code_many": 1, "file_inventory": 1},
		FileIndexRefreshes: 1,
	}
	redacted := security.NewSecretRedactor().RedactAny(source)
	if _, stillTyped := redacted.(repl.Result); stillTyped {
		t.Log("redactor preserved the typed result; the generic path stays covered below")
	}

	operations := replOperationsForJournal(redacted)
	if operations["count_code_many"] != 1 || operations["file_inventory"] != 1 {
		t.Fatalf("redacted operations = %#v, want the worker's counters", operations)
	}
	if refreshes := replFileIndexRefreshesForJournal(redacted); refreshes != 1 {
		t.Fatalf("redacted file index refreshes = %d, want 1", refreshes)
	}

	// Non-count payloads must not become evidence.
	if got := replOperationsForJournal(map[string]any{
		"operations": map[string]any{"count_code": "many", "file_stats": 1.5},
	}); len(got) != 0 {
		t.Fatalf("non-integer counters = %#v, want none", got)
	}
	if got := replFileIndexRefreshesForJournal(map[string]any{"file_index_refreshes": "1"}); got != 0 {
		t.Fatalf("string refresh count = %d, want 0", got)
	}
	if got := replOperationsForJournal(map[string]any{"stdout": "hello"}); got != nil {
		t.Fatalf("unrelated map = %#v, want nil", got)
	}
}

// TestExecutionHandlerJournalsREPLEvidence proves the whole chain end to end:
// the handler the builder installs must write the counters into the journal
// that eval scoring reads.
func TestExecutionHandlerJournalsREPLEvidence(t *testing.T) {
	workDir := t.TempDir()
	journal, err := NewExecutionJournal(workDir)
	if err != nil {
		t.Fatalf("NewExecutionJournal: %v", err)
	}
	a := &App{journal: journal}
	handler := a.buildExecutionHandler(nil)

	result := tools.NewSuccessResultWithData("kernel generation: 3", security.NewSecretRedactor().RedactAny(
		repl.Result{Operations: map[string]int{"count_code_many": 2}, FileIndexRefreshes: 1},
	))
	handler.OnToolEnd("repl_exec", map[string]any{"action": "execute"}, result)

	data, err := os.ReadFile(filepath.Join(workDir, ".gokin", "execution_journal.jsonl"))
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse journal line %q: %v", line, err)
		}
		if event["event"] != "tool_end" {
			continue
		}
		details, _ := event["details"].(map[string]any)
		operations, _ := details["repl_operations"].(map[string]any)
		if operations["count_code_many"] != float64(2) {
			t.Fatalf("journaled operations = %#v, want count_code_many=2", details["repl_operations"])
		}
		if details["repl_file_index_refreshes"] != float64(1) {
			t.Fatalf("journaled refreshes = %#v, want 1", details["repl_file_index_refreshes"])
		}
		found = true
	}
	if !found {
		t.Fatalf("no tool_end event in journal:\n%s", data)
	}
}
