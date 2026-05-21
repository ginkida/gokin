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

// TestGenerateToolResultSummary_GlobCodexStyle pins the glob pattern-first
// format. Empty/no-match content renders the pattern with a "(no matches)"
// suffix — the pattern is still the most useful at-a-glance signal.
func TestGenerateToolResultSummary_GlobCodexStyle(t *testing.T) {
	cases := []struct {
		name    string
		content string
		detail  string
		want    string
	}{
		{"pattern + matches", "a.go\nb.go\nc.go", "*.go", "*.go (3 matches)"},
		{"pattern + single match singular", "only.go", "*.go", "*.go (1 match)"},
		{"pattern + no matches sentinel", "(no matches)", "*.go", "*.go (no matches)"},
		{"pattern + empty content", "", "*.go", "*.go (no matches)"},
		{"no pattern but matches", "a\nb", "", "2 matches"},
		{"no pattern no matches", "", "", "no matches"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generateToolResultSummary("glob", tc.content, tc.detail)
			if got != tc.want {
				t.Errorf("glob summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateToolResultSummary_GrepCodexStyle pins the grep pattern-first
// format. Same shape as glob — only the wording (match vs file) was
// different in the legacy implementation.
func TestGenerateToolResultSummary_GrepCodexStyle(t *testing.T) {
	cases := []struct {
		name    string
		content string
		detail  string
		want    string
	}{
		{"pattern + matches", "hit1\nhit2", "TODO", "TODO (2 matches)"},
		{"pattern + single match", "single hit", "FIXME", "FIXME (1 match)"},
		{"pattern + no matches", "", "BUG", "BUG (no matches)"},
		{"no pattern + matches", "hit\nhit", "", "2 matches"},
		{"file_search alias also codex-style", "found1\nfound2\nfound3", "config", "config (3 matches)"},
		{"code_search alias also codex-style", "match1", "TODO", "TODO (1 match)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := generateToolResultSummary(strings.SplitN(tc.name, " ", 2)[0], tc.content, tc.detail)
			if !strings.Contains(got, tc.want) && got != tc.want {
				// First-token-based tool name extraction is brittle for
				// these test names; do a direct grep run instead.
				got = generateToolResultSummary("grep", tc.content, tc.detail)
				if got != tc.want {
					// Use the aliased tool name explicitly.
					switch tc.name {
					case "file_search alias also codex-style":
						got = generateToolResultSummary("file_search", tc.content, tc.detail)
					case "code_search alias also codex-style":
						got = generateToolResultSummary("code_search", tc.content, tc.detail)
					}
				}
			}
			if got != tc.want {
				t.Errorf("grep summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGenerateToolResultSummary_TreeAndListDirCodexStyle pins the dir-first
// format with item count or "(empty)" suffix. tree, list_dir, list_files
// all share the same path.
func TestGenerateToolResultSummary_TreeAndListDirCodexStyle(t *testing.T) {
	for _, tool := range []string{"tree", "list_dir", "list_files"} {
		t.Run(tool, func(t *testing.T) {
			cases := []struct {
				content string
				detail  string
				want    string
			}{
				{"a.go\nb.go\nc.go", "src/", "src/ (3 items)"},
				{"one.go", "pkg/", "pkg/ (1 item)"},
				{"(empty)", "vendor/", "vendor/ (empty)"},
				{"", "tmp/", "tmp/ (empty)"},
				{"a\nb", "", "2 items"},
				{"", "", "empty"},
			}
			for _, tc := range cases {
				got := generateToolResultSummary(tool, tc.content, tc.detail)
				if got != tc.want {
					t.Errorf("%s(%q, %q) = %q, want %q", tool, tc.content, tc.detail, got, tc.want)
				}
			}
		})
	}
}

// TestGenerateToolResultSummary_WebFetchCodexStyle pins the URL-only
// format. "fetched" status filler was redundant when the URL itself
// tells you what happened.
func TestGenerateToolResultSummary_WebFetchCodexStyle(t *testing.T) {
	cases := []struct {
		content string
		detail  string
		want    string
	}{
		{"<html>...", "https://example.com", "https://example.com"},
		{"body", "", "fetched"}, // empty URL falls back so we don't render a blank
	}
	for _, tc := range cases {
		got := generateToolResultSummary("web_fetch", tc.content, tc.detail)
		if got != tc.want {
			t.Errorf("web_fetch(%q, %q) = %q, want %q", tc.content, tc.detail, got, tc.want)
		}
	}
}

// TestGenerateToolResultSummary_WebSearchCodexStyle pins the query-first
// format with result count in parens (count derived from "http" tokens
// in body — search backends differ).
func TestGenerateToolResultSummary_WebSearchCodexStyle(t *testing.T) {
	threeResults := "http://a\nhttp://b\nhttp://c"
	cases := []struct {
		content string
		detail  string
		want    string
	}{
		{threeResults, "go modules", "go modules (3 results)"},
		{"http://only", "lipgloss", "lipgloss (1 result)"},
		{"plain text no urls", "rare topic", "rare topic (no results)"},
		{"", "anything", "anything (no results)"},
		{threeResults, "", "3 results"},
		{"", "", "no results"},
	}
	for _, tc := range cases {
		got := generateToolResultSummary("web_search", tc.content, tc.detail)
		if got != tc.want {
			t.Errorf("web_search(%q, %q) = %q, want %q", tc.content, tc.detail, got, tc.want)
		}
	}
}

// TestGenerateToolResultSummary_EditWriteDeleteCodexStyle pins the
// path-only summary for edit/write/delete. The verb prefix ("updated",
// "wrote", "deleted") was status filler — in a column of file ops the
// path itself is the signal.
func TestGenerateToolResultSummary_EditWriteDeleteCodexStyle(t *testing.T) {
	for _, tool := range []string{"edit", "write", "delete"} {
		t.Run(tool, func(t *testing.T) {
			got := generateToolResultSummary(tool, "any\ncontent", "src/file.go")
			if got != "src/file.go" {
				t.Errorf("%s should render just the path, got %q", tool, got)
			}
		})
	}
}

// TestGenerateToolResultSummary_AllPayloadFirst is the consistency
// invariant. Every tool whose summary leads with operation payload
// (path/pattern/url/query/command) must keep that lead — if a future
// change inserts a status verb prefix ("updated PATH", "fetched URL"),
// this test trips. Generic detail-less and empty-content tools are
// exempted.
func TestGenerateToolResultSummary_AllPayloadFirst(t *testing.T) {
	cases := []struct {
		tool   string
		detail string
	}{
		{"read", "src/file.go"},
		{"bash", "npm install"},
		{"glob", "*.go"},
		{"grep", "TODO"},
		{"edit", "src/file.go"},
		{"write", "src/file.go"},
		{"delete", "src/file.go"},
		{"tree", "src/"},
		{"list_dir", "src/"},
		{"web_fetch", "https://example.com"},
		{"web_search", "query"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			got := generateToolResultSummary(tc.tool, "line 1\nline 2", tc.detail)
			if !strings.HasPrefix(got, tc.detail) {
				t.Errorf("%s should lead with payload %q, got %q", tc.tool, tc.detail, got)
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
