package context

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// TestBuildRegroundingNote pins the v0.86.2 fix: after a hard EmergencyTruncate
// (which keeps only history[:2] + recent tail and drops the original task at
// ~history[2]), a re-grounding note carries the task + key files forward so the
// model doesn't drift off-task.
func TestBuildRegroundingNote(t *testing.T) {
	m := &ContextManager{keyFiles: map[string]bool{"z/late.go": true, "a/early.go": true}}

	history := []*genai.Content{
		genai.NewContentFromText("you are gokin", genai.RoleModel), // system-ish, not user
		genai.NewContentFromText("Implement the JSON parser and add tests", genai.RoleUser),
		genai.NewContentFromText("working on it", genai.RoleModel),
	}

	note := m.buildRegroundingNote(history)
	if note == nil {
		t.Fatal("buildRegroundingNote returned nil for a history with a task")
	}
	if note.Role != genai.RoleUser {
		t.Errorf("re-grounding note role = %v, want User", note.Role)
	}
	text := note.Parts[0].Text
	if !strings.Contains(text, "Implement the JSON parser") {
		t.Errorf("note missing the original task: %q", text)
	}
	if !strings.Contains(text, "a/early.go") || !strings.Contains(text, "z/late.go") {
		t.Errorf("note missing key files: %q", text)
	}
	// Key files must be sorted for deterministic output.
	if strings.Index(text, "a/early.go") > strings.Index(text, "z/late.go") {
		t.Error("key files not sorted in the note")
	}

	// No user message → nil (nothing useful to re-ground with).
	noTask := []*genai.Content{genai.NewContentFromText("only a model turn", genai.RoleModel)}
	if got := m.buildRegroundingNote(noTask); got != nil {
		t.Errorf("expected nil for history without a user task, got %+v", got)
	}

	// A very long task is truncated (doesn't blow the budget it's trying to save).
	long := strings.Repeat("x", 2000)
	big := []*genai.Content{genai.NewContentFromText(long, genai.RoleUser)}
	n := m.buildRegroundingNote(big)
	if n == nil || len([]rune(n.Parts[0].Text)) > 900 {
		t.Errorf("long task not truncated: note rune len = %d", len([]rune(n.Parts[0].Text)))
	}
}
