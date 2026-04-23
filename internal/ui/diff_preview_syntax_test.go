package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Sanity: context lines now carry ANSI escape codes from chroma syntax
// highlighting. Before v0.70.7 they were flat-grey contextStyle-only.
// Users on a color-capable terminal see keywords/strings/comments
// colored just like in Claude Code.
func TestRenderContextLineSyntax_AddsSyntaxAnsiForKnownLang(t *testing.T) {
	contextStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	// Go line with keywords and identifiers — chroma should emit color
	// escapes for `func`, identifiers, etc.
	rendered := renderContextLineSyntax(" func Foo() {}", "go", contextStyle)
	if !strings.Contains(rendered, "\x1b[") {
		t.Errorf("expected ANSI escape from chroma for Go context line, got: %q", rendered)
	}
	// Leading space (the diff context marker) must survive so column
	// alignment with +/- lines doesn't break.
	if !strings.HasPrefix(stripAnsi(rendered), " ") {
		t.Errorf("leading diff-context space lost: stripped=%q", stripAnsi(rendered))
	}
}

// Empty lines bypass chroma: returns empty string directly. Prevents
// spurious chroma output for blank separator lines in the diff.
func TestRenderContextLineSyntax_EmptyLinePassesThrough(t *testing.T) {
	contextStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	if got := renderContextLineSyntax("", "go", contextStyle); got != "" {
		t.Errorf("empty line should produce empty output, got %q", got)
	}
}

// Whitespace-only context lines (e.g. " ") render with the plain
// context style rather than going through chroma — a pure-space line
// has no tokens to color, and chroma's output for it can include
// stray escapes that waste space.
func TestRenderContextLineSyntax_WhitespaceOnlyKeepsContextStyle(t *testing.T) {
	contextStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	rendered := renderContextLineSyntax(" ", "go", contextStyle)
	if rendered == "" {
		t.Errorf("whitespace line must not vanish; got empty")
	}
	if !strings.HasPrefix(stripAnsi(rendered), " ") {
		t.Errorf("whitespace content lost: %q", stripAnsi(rendered))
	}
}

// Unknown language (empty lang from DetectLanguage) must not crash or
// produce an empty line — chroma falls through to a generic lexer
// internally; if even that yields empty, we fall back to contextStyle.
func TestRenderContextLineSyntax_UnknownLanguageFallback(t *testing.T) {
	contextStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	rendered := renderContextLineSyntax(" some arbitrary text", "", contextStyle)
	if rendered == "" {
		t.Errorf("fallback path must render something non-empty, got empty")
	}
	// Content must be preserved after stripping ANSI.
	if !strings.Contains(stripAnsi(rendered), "some arbitrary text") {
		t.Errorf("content lost in fallback: %q", stripAnsi(rendered))
	}
}

// End-to-end: highlightDiff on a DiffPreviewModel with a Go filePath
// must emit colored context lines. Before this fix, context lines were
// uniformly grey regardless of language.
func TestDiffPreviewModel_HighlightDiffIncludesSyntaxColors(t *testing.T) {
	m := &DiffPreviewModel{
		filePath: "foo.go",
	}
	diff := strings.Join([]string{
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,3 @@",
		" package main",
		"-func Old() {}",
		"+func New() {}",
		" ",
	}, "\n")
	out := m.highlightDiff(diff)
	// "package main" context line should carry chroma escape codes
	// distinct from the flat-gray contextStyle.
	if !strings.Contains(out, "package") {
		t.Errorf("expected rendered output to contain 'package'")
	}
	// At least one chroma-specific 256-color escape should appear on
	// context lines (38;5; prefix is how chroma emits 256-color fg).
	// If chroma was a no-op, we'd only see the single #9CA3AF RGB code.
	contextLineSnippet := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(stripAnsi(line), "package main") {
			contextLineSnippet = line
			break
		}
	}
	if contextLineSnippet == "" {
		t.Fatalf("could not locate rendered 'package main' context line in output")
	}
	// Look for multiple ANSI escapes indicating multi-token highlighting
	// (keyword + identifier = at least 2 color changes).
	escapeCount := strings.Count(contextLineSnippet, "\x1b[")
	if escapeCount < 2 {
		t.Errorf("expected ≥2 ANSI escapes on 'package main' context line (chroma keyword+ident), got %d: %q",
			escapeCount, contextLineSnippet)
	}
}

// Non-code files (no known extension) must NOT crash or produce garbage.
// A README.md diff should render with generic colors.
func TestDiffPreviewModel_HighlightDiffFallsBackForUnknownExt(t *testing.T) {
	m := &DiffPreviewModel{
		filePath: "NOTES.unknown_ext",
	}
	diff := strings.Join([]string{
		"--- a/NOTES.unknown_ext",
		"+++ b/NOTES.unknown_ext",
		"@@ -1 +1 @@",
		"-old note",
		"+new note",
	}, "\n")
	out := m.highlightDiff(diff)
	// Must include the content, not crash or return empty.
	if !strings.Contains(stripAnsi(out), "old note") || !strings.Contains(stripAnsi(out), "new note") {
		t.Errorf("unknown-ext diff lost content: %q", stripAnsi(out))
	}
}

// MultiDiffPreviewModel.highlightMultiDiff must apply per-file language
// detection to context lines. Before v0.70.7 the multi-diff path used
// only flat contextStyle even on .go files.
func TestMultiDiffPreviewModel_HighlightUsesFileLanguage(t *testing.T) {
	m := &MultiDiffPreviewModel{
		files: []DiffFile{
			{FilePath: "foo.go", Diff: ""},
			{FilePath: "bar.txt", Diff: ""},
		},
		currentIndex: 0,
	}
	diff := strings.Join([]string{
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,1 +1,1 @@",
		" package main",
	}, "\n")
	out := m.highlightMultiDiff(diff)

	// Locate the rendered "package main" context line and assert it
	// contains chroma escapes (not just the flat contextStyle color).
	var contextSnippet string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(stripAnsi(ln), "package main") {
			contextSnippet = ln
			break
		}
	}
	if contextSnippet == "" {
		t.Fatalf("context line not found in multi-diff output: %q", out)
	}
	if strings.Count(contextSnippet, "\x1b[") < 2 {
		t.Errorf("multi-diff context line missing syntax highlighting; expected ≥2 ANSI escapes, got: %q", contextSnippet)
	}
}

// MultiDiff: out-of-range currentIndex must not crash. Empty files
// slice is a legit state during construction / reset.
func TestMultiDiffPreviewModel_EmptyFilesNoCrash(t *testing.T) {
	m := &MultiDiffPreviewModel{
		files:        nil,
		currentIndex: 0,
	}
	// Should not panic — just produce diff without language-aware highlighting.
	diff := " plain context line\n"
	out := m.highlightMultiDiff(diff)
	if out == "" {
		t.Error("empty-files path must still render the diff")
	}
}

// Pin: +/- lines keep their original flat green/red styling. Layering
// chroma on top would cause ANSI-on-ANSI background collisions. The
// word-diff highlight carries enough signal for edited lines.
func TestDiffPreviewModel_AddedAndRemovedLinesDontGetChroma(t *testing.T) {
	m := &DiffPreviewModel{filePath: "foo.go"}
	// Single-sided change (no pair) — deterministic output.
	diff := strings.Join([]string{
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -0,0 +1,1 @@",
		"+var added = 1",
	}, "\n")
	out := m.highlightDiff(diff)

	// Locate the "+var added = 1" line.
	var addedLine string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(stripAnsi(ln), "var added = 1") {
			addedLine = ln
			break
		}
	}
	if addedLine == "" {
		t.Fatalf("added line not rendered: %q", out)
	}
	// Should have exactly ONE style applied (the flat addedStyle green
	// + bold). If chroma ran, we'd see multiple escapes for keyword
	// highlighting inside the line.
	//
	// addedStyle emits [fg][bold]text[reset] = 3 escape sequences.
	// Any significantly higher count would indicate chroma ran.
	escapeCount := strings.Count(addedLine, "\x1b[")
	if escapeCount > 4 {
		t.Errorf("added line has %d ANSI escapes — chroma should NOT run on +/- lines: %q",
			escapeCount, addedLine)
	}
}
