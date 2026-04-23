package ui

import (
	"strings"
	"testing"
)

// TestFirstSignalLine_SkipsCodePreamble pins the heuristic that turns a
// Go-file preview from "package ui / (blank) / import (" into something
// with actual signal like the first func/type declaration. This is the
// exact UX complaint that triggered the smart-preview work.
func TestFirstSignalLine_SkipsCodePreamble(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name: "go_file_with_import_block",
			lines: []string{
				"package ui",
				"",
				"import (",
				`  "fmt"`,
				`  "strings"`,
				")",
				"",
				"func First() {}",
			},
			want: 7, // "func First()"
		},
		{
			name: "go_file_inline_imports",
			lines: []string{
				"package ui",
				`import "fmt"`,
				`import "strings"`,
				"",
				"type Thing struct {}",
			},
			want: 4, // "type Thing struct"
		},
		{
			name: "python_file",
			lines: []string{
				"import os",
				"from pathlib import Path",
				"",
				"def do_thing():",
			},
			want: 3, // "def do_thing"
		},
		{
			name: "no_preamble_stays_at_zero",
			lines: []string{
				"func Alpha() {}",
				"func Beta() {}",
			},
			want: 0,
		},
		{
			name:  "only_package_no_imports",
			lines: []string{"package ui", "func A() {}"},
			want:  1,
		},
		{
			name:  "empty_input_zero",
			lines: []string{},
			want:  0,
		},
		{
			name:  "all_blanks_returns_zero",
			lines: []string{"", "", ""},
			want:  0,
		},
		{
			name: "c_header_style_includes",
			lines: []string{
				`#include <stdio.h>`,
				`#include "local.h"`,
				"",
				"int main(void) {}",
			},
			want: 3,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstSignalLine(c.lines); got != c.want {
				t.Errorf("firstSignalLine = %d, want %d\nlines = %#v", got, c.want, c.lines)
			}
		})
	}
}

// TestLastSignalLine_SkipsClosingBraces: the trailing `}` + trailing
// blanks contribute zero signal, so lastSignalLine rewinds past them.
func TestLastSignalLine_SkipsClosingBraces(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{
			name: "trailing_brace_and_blank",
			lines: []string{
				"func Foo() {",
				"  doStuff()",
				"}",
				"",
			},
			want: 1, // "doStuff()" — skip blank and }
		},
		{
			name: "trailing_closing_paren",
			lines: []string{
				"var block = []int{",
				"  1, 2, 3,",
				"}",
				"",
				"",
			},
			want: 1,
		},
		{
			name: "no_trailing_noise",
			lines: []string{
				"func Foo() {}",
				"return nil",
			},
			want: 1,
		},
		{
			name: "all_braces_returns_zero",
			lines: []string{"}", "}", ""},
			want:  0, // guard: don't slide past index 0
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lastSignalLine(c.lines); got != c.want {
				t.Errorf("lastSignalLine = %d, want %d\nlines = %#v", got, c.want, c.lines)
			}
		})
	}
}

// TestGenerateToolResultSummary_ShortensPath guards the regression fix
// for duplicated long paths between the exec header and the success
// summary. For read/write/edit/glob/grep the summary must NOT echo a
// full absolute path back at the user — shortenPath substitutes `~/…`
// or middle-ellipsis so the filename stays visible but the prefix
// doesn't dominate the status line.
func TestGenerateToolResultSummary_ShortensPath(t *testing.T) {
	// Assumes the filename is preserved at the tail — that's the
	// shortenPath contract. We don't pin exact output because it
	// depends on the test runner's $HOME, but we verify:
	//   - the output is shorter than the input
	//   - the basename is still visible (so users can identify file)
	longPath := "/Users/alice/github/project/with/deep/nesting/internal/ui/tui.go"
	content := "line 1\nline 2\nline 3"
	got := generateToolResultSummary("read", content, longPath)

	if !strings.Contains(got, "tui.go") {
		t.Errorf("basename 'tui.go' missing from summary: %q", got)
	}
	// The long prefix should not appear verbatim — shortenPath collapses it.
	if strings.Contains(got, "/Users/alice/github/project/with/deep/nesting") {
		t.Errorf("unshortened long prefix leaked into summary: %q", got)
	}
	// Line count must still land.
	if !strings.Contains(got, "3 lines") {
		t.Errorf("line count missing: %q", got)
	}
}

// TestRenderTruncated_HidesCodePreamble verifies the end-to-end preview
// rendering — a Go-style file shouldn't waste the head budget on package
// declarations and empty lines.
func TestRenderTruncated_HidesCodePreamble(t *testing.T) {
	m := NewToolOutputModel(DefaultStyles())
	m.SetConfig(ToolOutputConfig{
		MaxCollapsedLines: 6,
		HeadRatio:         0.66,
	})

	content := strings.Join([]string{
		"package ui",       // index 0 — skipped
		"",                 // 1 — skipped
		"import (",         // 2 — skipped
		`  "fmt"`,          // 3 — skipped
		")",                // 4 — skipped
		"",                 // 5 — skipped
		"func First() {}",  // 6 — first signal
		"func Second() {}", // 7
		"func Third() {}",  // 8
		"func Fourth() {}", // 9
		"func Fifth() {}",  // 10
		"func Sixth() {}",  // 11
		"func Seventh() {}", // 12
		"func Eighth() {}", // 13
		"}",                // 14 — skipped as trailing
	}, "\n")

	out := m.renderTruncated(content)

	// Head must start at func First, NOT at package.
	if strings.Contains(out, "package ui") {
		t.Errorf("preview leaked 'package ui' — preamble skip broken:\n%s", out)
	}
	if !strings.Contains(out, "func First()") {
		t.Errorf("preview should start at first signal line:\n%s", out)
	}
	// Must include the hidden-count marker. The exact glyph is optional
	// (can drift across UI polish passes) but the *count* of hidden
	// lines + the action hint ("press e to expand") are the load-bearing
	// pieces the user relies on.
	if !strings.Contains(out, "more") || !strings.Contains(out, "press e") {
		t.Errorf("missing 'N more lines · press e' marker:\n%s", out)
	}
}

// TestRenderHiddenLinesHint_Pluralisation pins the "line" vs "lines"
// singular/plural switching so we don't ship "1 more lines" to users.
func TestRenderHiddenLinesHint_Pluralisation(t *testing.T) {
	cases := []struct {
		hidden int
		want   string
	}{
		{1, "1 more line ·"}, // singular
		{2, "2 more lines ·"},
		{95, "95 more lines ·"},
	}
	for _, c := range cases {
		got := renderHiddenLinesHint(c.hidden)
		if !strings.Contains(got, c.want) {
			t.Errorf("hidden=%d: want contains %q, got %q", c.hidden, c.want, got)
		}
	}
	// Zero or negative → empty output, no fallback text.
	if got := renderHiddenLinesHint(0); got != "" {
		t.Errorf("hidden=0 should render empty, got %q", got)
	}
	if got := renderHiddenLinesHint(-5); got != "" {
		t.Errorf("hidden<0 should render empty, got %q", got)
	}
}
