package evals

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeJournal creates <workspace>/.gokin/execution_journal.jsonl with the
// given lines.
func writeJournal(t *testing.T, workspace string, lines []string) {
	t.Helper()
	dir := filepath.Join(workspace, ".gokin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "execution_journal.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want || strings.HasSuffix(s, "/"+want) {
			return true
		}
	}
	return false
}

// A single legitimately-large journal line (bigger than the old 2 MB Scanner
// token cap, smaller than maxJournalLineBytes) must NOT abort the scan: every
// line AFTER it must still be parsed. Pre-fix, the Scanner returned false on the
// oversized line and silently dropped the remainder.
func TestSummarizeExecutionJournal_LargeLineDoesNotAbortScan(t *testing.T) {
	ws := t.TempDir()
	// ~2.5 MB command arg — over the old 2 MB Scanner cap, under the 8 MiB guard.
	big := strings.Repeat("x", 2_500_000)
	lines := []string{
		`{"event":"tool_start","details":{"tool":"read","args":{"file_path":"a.go"}}}`,
		`{"event":"tool_start","details":{"tool":"bash","args":{"command":"echo ` + big + `"}}}`,
		`{"event":"tool_start","details":{"tool":"edit","args":{"file_path":"c.go"}}}`,
	}
	writeJournal(t, ws, lines)

	summary := summarizeExecutionJournal(ws, "", nil)
	if summary == nil {
		t.Fatal("expected a journal summary, got nil")
	}
	// The line AFTER the huge one must have been parsed.
	if !contains(summary.Tools, "edit") {
		t.Errorf("edit tool from the line after the large line was dropped; tools=%v", summary.Tools)
	}
	if !contains(summary.FilesEdited, "c.go") {
		t.Errorf("c.go edit after the large line was dropped; filesEdited=%v", summary.FilesEdited)
	}
	if summary.ToolCalls != 3 {
		t.Errorf("expected 3 tool calls parsed, got %d", summary.ToolCalls)
	}
}

// A line above maxJournalLineBytes is skipped (recorded as a parse error) but
// the scan continues past it.
func TestSummarizeExecutionJournal_OversizedLineSkippedNotFatal(t *testing.T) {
	ws := t.TempDir()
	huge := strings.Repeat("y", maxJournalLineBytes+16)
	lines := []string{
		`{"event":"tool_start","details":{"tool":"read","args":{"file_path":"a.go"}}}`,
		`{"event":"tool_start","details":{"tool":"bash","args":{"command":"` + huge + `"}}}`,
		`{"event":"tool_start","details":{"tool":"edit","args":{"file_path":"c.go"}}}`,
	}
	writeJournal(t, ws, lines)

	summary := summarizeExecutionJournal(ws, "", nil)
	if summary == nil {
		t.Fatal("expected a journal summary, got nil")
	}
	if !contains(summary.FilesEdited, "c.go") {
		t.Errorf("line after the oversized line was dropped; filesEdited=%v", summary.FilesEdited)
	}
	foundSkip := false
	for _, pe := range summary.ParseErrors {
		if strings.Contains(pe, "oversized") {
			foundSkip = true
		}
	}
	if !foundSkip {
		t.Errorf("expected an 'oversized' parse-error note; parseErrors=%v", summary.ParseErrors)
	}
}

// A batch tool call's explicit `files` list must be recorded as edited paths.
// Pre-fix, appendBatchEditedPaths parsed a nested tools/calls array the real
// batch tool never emits, so batch-driven edits were never counted.
func TestSummarizeExecutionJournal_BatchFilesRecorded(t *testing.T) {
	ws := t.TempDir()
	lines := []string{
		`{"event":"tool_start","details":{"tool":"batch","args":{"operation":"replace","files":["x.go","y.go"],"search":"a","replacement":"b"}}}`,
	}
	writeJournal(t, ws, lines)

	summary := summarizeExecutionJournal(ws, "", nil)
	if summary == nil {
		t.Fatal("expected a journal summary, got nil")
	}
	if !contains(summary.FilesEdited, "x.go") || !contains(summary.FilesEdited, "y.go") {
		t.Errorf("batch files not recorded as edited; filesEdited=%v", summary.FilesEdited)
	}
}
