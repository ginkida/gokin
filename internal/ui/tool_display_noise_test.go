package ui

import (
	"strings"
	"testing"
)

// Machine-facing context blocks are load-bearing for the MODEL but read as
// noise in the user's scrollback ("[context:predicted] Related files: …",
// the edit tool's "no verification read needed" instruction). They must be
// stripped from every user-facing surface while the model-facing stream
// stays untouched.

func TestStripModelFacingContext(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "predicted enrichment line dropped",
			in:   "Created new file: /w/x.go (23774 bytes)\n\n[context:predicted] Related files: store.go (pattern match): package memory | import (",
			want: "Created new file: /w/x.go (23774 bytes)",
		},
		{
			name: "plain context hint dropped",
			in:   "ok\n\n[context] go.mod specifies go 1.25",
			want: "ok",
		},
		{
			name: "edit region header cleaned, snippet kept",
			in:   "Applied 7 edit(s) to /w/x.go\n\nUpdated region (already written to disk — no verification read needed):\n  10│ foo\n  11│ bar",
			want: "Applied 7 edit(s) to /w/x.go\n\nUpdated region:\n  10│ foo\n  11│ bar",
		},
		{
			name: "ordinary content untouched",
			in:   "line one\nline two",
			want: "line one\nline two",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}
	for _, c := range cases {
		if got := stripModelFacingContext(c.in); got != c.want {
			t.Errorf("%s:\n got: %q\nwant: %q", c.name, got, c.want)
		}
	}
}

// End-to-end: a tool result flowing through the FULL Update path must reach
// the scrollback without the machine-facing block.
func TestToolResultMsg_StripsContextNoiseFromScrollback(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.state = StateProcessing

	m.handleMessageTypes(ToolCallMsg{Name: "write", Args: map[string]any{"file_path": "/w/x.go"}})
	m.handleMessageTypes(ToolResultMsg{
		Name:    "write",
		Args:    map[string]any{"file_path": "/w/x.go"},
		Content: "Created new file: /w/x.go (100 bytes)\n\n[context:predicted] Related files: store.go (pattern match): package memory",
	})

	rendered := stripAnsi(m.output.state.content.String())
	if strings.Contains(rendered, "[context") {
		t.Fatalf("machine-facing enrichment leaked into scrollback:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Created new file") {
		t.Fatalf("real result content must survive the strip:\n%s", rendered)
	}
}

// The bash target must strip the display "$ " prefix BEFORE the `cd … &&`
// plumbing check — otherwise the whole scaffolding survives and the 48-char
// cap truncates the actual command ("Bash($ cd /long/path && go test -...)").
func TestConciseToolTarget_BashDollarPrefixAndCd(t *testing.T) {
	got := conciseToolTarget("bash", "$ cd /Users/x/github/gokin && go test -race ./internal/ui/")
	if strings.Contains(got, "cd ") || strings.Contains(got, "$") {
		t.Fatalf("plumbing survived: %q", got)
	}
	if !strings.HasPrefix(got, "go test -race") {
		t.Fatalf("command intent must lead the target: %q", got)
	}
}
