package ui

import (
	"strings"
	"testing"
	"time"
)

func TestToolIsUndoable(t *testing.T) {
	for _, tn := range []string{"edit", "write", "delete", "move", "copy", "refactor", "mkdir", "batch", "Edit", "WRITE"} {
		if !toolIsUndoable(tn) {
			t.Errorf("%q should be undoable", tn)
		}
	}
	for _, tn := range []string{"bash", "read", "grep", "glob", "ls", "tree", "web_fetch", ""} {
		if toolIsUndoable(tn) {
			t.Errorf("%q should NOT be undoable", tn)
		}
	}
}

func TestRenderErrorActionHints_OnlyHonestUndoForMutating(t *testing.T) {
	// Mutating tool: the one genuinely-actionable recovery (/undo) is offered.
	editHint := stripAnsi(renderErrorActionHints("edit"))
	if !strings.Contains(editHint, "/undo") {
		t.Errorf("edit error hint should offer /undo: %q", editHint)
	}
	// No fake "retry" keystroke (there's no StateToolError handler for it).
	if strings.Contains(editHint, "retry") {
		t.Errorf("error hint must not promise a non-existent retry action: %q", editHint)
	}

	// Read-only tool failure: no honest tool-specific action ⇒ empty hint row
	// (card collapses to just title + detail).
	for _, tn := range []string{"bash", "read", "grep"} {
		if h := renderErrorActionHints(tn); h != "" {
			t.Errorf("%s error hint should be empty (nothing to revert/retry), got %q", tn, h)
		}
	}
}

func TestGenerateToolResultSummary_BashNoMatches(t *testing.T) {
	got := generateToolResultSummary("bash", "(no matches)", "grep -n RPS file.go")
	if !strings.Contains(got, "no matches") {
		t.Errorf("bash no-match summary should read 'no matches', got %q", got)
	}
	if strings.Contains(got, "line") {
		t.Errorf("bash no-match summary should not show a line count, got %q", got)
	}
}

func TestFormatToolSuccessBlock_HidesTrivialDuration(t *testing.T) {
	s := &Styles{}

	fast := stripAnsi(s.FormatToolSuccessBlock("read", 9*time.Millisecond, "file.go (178 lines)"))
	if strings.Contains(fast, "9ms") {
		t.Errorf("sub-100ms duration should be hidden: %q", fast)
	}
	if !strings.Contains(fast, "file.go (178 lines)") {
		t.Errorf("summary should still render: %q", fast)
	}

	slow := stripAnsi(s.FormatToolSuccessBlock("bash", 1100*time.Millisecond, "cmd (40 lines)"))
	if !strings.Contains(slow, "1.1s") {
		t.Errorf("noteworthy (≥100ms) duration should be shown: %q", slow)
	}

	// Exactly at the threshold: shown.
	boundary := stripAnsi(s.FormatToolSuccessBlock("bash", 100*time.Millisecond, ""))
	if !strings.Contains(boundary, "100ms") {
		t.Errorf("100ms boundary should be shown: %q", boundary)
	}
}
