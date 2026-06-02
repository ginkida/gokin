package tools

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestMaxStagnationRecoveryAttempts(t *testing.T) {
	cases := map[string]int{
		"read":     3,
		"grep":     3,
		"glob":     3,
		"list_dir": 3,
		"tree":     3,
		"edit":     1,
		"write":    0,
		"bash":     0,
		"delete":   0,
		"git_pr":   0,
		"":         0,
	}
	for tool, want := range cases {
		if got := maxStagnationRecoveryAttempts(tool); got != want {
			t.Errorf("maxStagnationRecoveryAttempts(%q) = %d, want %d", tool, got, want)
		}
	}
}

func TestShouldAttemptStagnationRecovery(t *testing.T) {
	read := []*genai.FunctionCall{{Name: "read"}}
	edit := []*genai.FunctionCall{{Name: "edit"}}
	bash := []*genai.FunctionCall{{Name: "bash"}}
	mixedReadBash := []*genai.FunctionCall{{Name: "read"}, {Name: "bash"}}
	mixedReadEdit := []*genai.FunctionCall{{Name: "read"}, {Name: "edit"}}

	cases := []struct {
		name     string
		calls    []*genai.FunctionCall
		attempts int
		want     bool
	}{
		{"empty batch never recovers", nil, 0, false},
		{"nil call never recovers", []*genai.FunctionCall{nil}, 0, false},
		{"read attempt 0", read, 0, true},
		{"read attempt 2 still within budget", read, 2, true},
		{"read attempt 3 budget spent", read, 3, false},
		{"edit attempt 0", edit, 0, true},
		{"edit attempt 1 budget spent", edit, 1, false},
		{"bash gets no recovery (model-agnostic, conservative)", bash, 0, false},
		{"mixed read+bash aborts (any non-recoverable)", mixedReadBash, 0, false},
		{"mixed read+edit uses most restrictive budget (1)", mixedReadEdit, 0, true},
		{"mixed read+edit attempt 1 budget spent", mixedReadEdit, 1, false},
	}
	for _, c := range cases {
		if got := shouldAttemptStagnationRecovery(c.calls, c.attempts); got != c.want {
			t.Errorf("%s: shouldAttemptStagnationRecovery(_, %d) = %v, want %v", c.name, c.attempts, got, c.want)
		}
	}
}

func TestBuildStagnationRecoveryMessage_ToolClass(t *testing.T) {
	const breakPhrase = "Do not call it again"

	read := buildStagnationRecoveryMessage("read", map[string]any{"file_path": "models.go", "offset": 58.0, "limit": 20.0}, 5)
	if !strings.Contains(read, breakPhrase) || !strings.Contains(read, "already have this file's content") {
		t.Errorf("read message missing reuse guidance: %q", read)
	}

	grep := buildStagnationRecoveryMessage("grep", map[string]any{"pattern": "TODO", "path": "internal"}, 5)
	if !strings.Contains(grep, breakPhrase) || !strings.Contains(grep, "results are already above") {
		t.Errorf("grep message missing reuse guidance: %q", grep)
	}

	edit := buildStagnationRecoveryMessage("edit", map[string]any{"file_path": "models.go", "old_string": "x"}, 5)
	if !strings.Contains(edit, breakPhrase) || !strings.Contains(edit, "old_string") || !strings.Contains(edit, "whitespace-sensitive") {
		t.Errorf("edit (old_string mode) message missing match-failure guidance: %q", edit)
	}

	// Non-old_string edit (line-range mode) gets coordinate-oriented guidance,
	// not the misleading "old_string isn't matching" advice.
	editLine := buildStagnationRecoveryMessage("edit", map[string]any{"file_path": "models.go", "line_start": 10.0, "line_end": 15.0}, 5)
	if !strings.Contains(editLine, breakPhrase) || !strings.Contains(editLine, "line numbers") {
		t.Errorf("edit (line-range mode) message missing coordinate guidance: %q", editLine)
	}
	if strings.Contains(editLine, "old_string") {
		t.Errorf("line-range edit message should not mention old_string: %q", editLine)
	}

	def := buildStagnationRecoveryMessage("bash", map[string]any{"command": "ls"}, 5)
	if !strings.Contains(def, breakPhrase) {
		t.Errorf("default message missing loop-break phrase: %q", def)
	}
}
