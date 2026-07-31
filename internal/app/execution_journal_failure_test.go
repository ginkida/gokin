package app

import "testing"

func TestExecutionJournalAppendReturnsPersistenceFailure(t *testing.T) {
	journal := &ExecutionJournal{
		journalPath: t.TempDir(),
	}
	if err := journal.Append("tool_start", map[string]any{"tool": "write"}); err == nil {
		t.Fatal("Append hid execution-journal persistence failure")
	}
}
