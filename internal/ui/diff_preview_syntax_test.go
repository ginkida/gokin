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
