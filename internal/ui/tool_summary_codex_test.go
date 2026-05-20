package ui

import (
	"strings"
	"testing"
)

// TestGenerateToolResultSummary_ReadCodexStyle pins the codex-style summary
// for read: path first, line count in parens. The old format was
// "N lines from path" which led with the count — fine for technical
// density, noisier in an exploration phase where the agent reads many
// files in a row. Leading with the path lets a column of read calls scan
// as a column of paths rather than a column of counts.
func TestGenerateToolResultSummary_ReadCodexStyle(t *testing.T) {
	cases := []struct {
		name    string
		content string
		detail  string
		want    string
	}{
		{
			name:    "path with multi-line content",
			content: "line 1\nline 2\nline 3",
			detail:  "src/file.go",
			want:    "src/file.go (3 lines)",
		},
		{
			name:    "path with single line",
			content: "only line",
			detail:  "config.yaml",
			want:    "config.yaml (1 line)",
		},
		{
			name:    "empty content with path",
			content: "",
			detail:  "empty.txt",
			want:    "empty.txt",
		},
		{
			name:    "content without detail",
			content: "line 1\nline 2",
			detail:  "",
			want:    "2 lines",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generateToolResultSummary("read", tc.content, tc.detail)
			if got != tc.want {
				t.Errorf("read summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateToolResultSummary_BashCodexStyle pins the codex-style summary
// for bash: command first, line count in parens. With body preview now
// collapsed by default (see collapsedByDefault in tui.go), this title is
// the only visible signal — leading with the command makes a stack of
// 5 bash calls scan as a column of operations.
func TestGenerateToolResultSummary_BashCodexStyle(t *testing.T) {
	cases := []struct {
		name    string
		content string
		detail  string
		want    string
	}{
		{
			name:    "command with multi-line output",
			content: "result line 1\nresult line 2\nresult line 3",
			detail:  "npm install",
			want:    "npm install (3 lines)",
		},
		{
			name:    "command with single-line output",
			content: "hello",
			detail:  "echo hello",
			want:    "echo hello (1 line)",
		},
		{
			name:    "command with no output",
			content: "",
			detail:  "mkdir -p tmp",
			want:    "mkdir -p tmp",
		},
		{
			name:    "empty everything falls back to 'completed'",
			content: "",
			detail:  "",
			want:    "completed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generateToolResultSummary("bash", tc.content, tc.detail)
			if got != tc.want {
				t.Errorf("bash summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateToolResultSummary_CollapseDefaultsAreLoadBearing checks that
// `collapsedByDefault` lists exactly the tools whose summary leads with the
// operation payload (path/command) rather than line count. If a future
// change adds bash but forgets to update the summary format — or vice
// versa — the assertion catches the mismatch: collapsed tools must have
// a summary that's useful WITHOUT the body preview.
func TestGenerateToolResultSummary_CollapseDefaultsAreLoadBearing(t *testing.T) {
	for _, tool := range []string{"read", "bash"} {
		if !collapsedByDefault(tool) {
			t.Errorf("%q should be in collapsedByDefault — its summary is designed to carry the signal alone", tool)
		}
		// Generated summary should contain the detail (operation payload)
		// when detail is provided.
		got := generateToolResultSummary(tool, "some\noutput", "my-detail")
		if !strings.Contains(got, "my-detail") {
			t.Errorf("%s summary should lead with detail, got %q", tool, got)
		}
	}
}
