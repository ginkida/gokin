package context

import (
	"strings"
	"testing"
)

// TestEnsureCriticalContext pins the summary re-anchoring: when a TaskContext is
// set, files/task the LLM summary dropped get re-anchored; present ones are not
// duplicated; no TaskContext is a no-op.
func TestEnsureCriticalContext(t *testing.T) {
	s := NewSummarizer(nil)
	s.SetTaskContext(&TaskContext{
		Title:         "fix the login bug",
		ArtifactPaths: []string{"internal/app/app.go", "internal/client/anthropic.go"},
	})

	// Lossy summary: kept one file, dropped the other + the task title.
	got := s.ensureCriticalContext("- worked on internal/app/app.go")
	if !strings.Contains(got, "fix the login bug") {
		t.Error("dropped task title should be re-anchored")
	}
	if !strings.Contains(got, "internal/client/anthropic.go") {
		t.Error("dropped file should be re-anchored")
	}
	if c := strings.Count(got, "internal/app/app.go"); c != 1 {
		t.Errorf("present file must not be re-duplicated, count=%d", c)
	}

	// Complete summary → unchanged (no re-anchor block).
	full := "Task fix the login bug — touched internal/app/app.go and internal/client/anthropic.go"
	if out := s.ensureCriticalContext(full); out != full {
		t.Errorf("complete summary should be unchanged, got %q", out)
	}

	// No TaskContext → no-op.
	s.SetTaskContext(nil)
	if out := s.ensureCriticalContext("anything"); out != "anything" {
		t.Errorf("no TaskContext should be a no-op, got %q", out)
	}
}
