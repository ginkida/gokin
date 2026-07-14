package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestSearchResultsEmptyStateAndFooter(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetResults("needle", "grep", nil)

	view := stripAnsi(m.View())
	for _, want := range []string{`No matches for "needle"`, "Try a broader pattern", "Esc/q Close", "(0 results)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty search view missing %q:\n%s", want, view)
		}
	}
	for _, unavailable := range []string{"Open", "Edit", "Copy path", "Show preview", "Navigate"} {
		if strings.Contains(view, unavailable) {
			t.Fatalf("empty search advertises unavailable action %q:\n%s", unavailable, view)
		}
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if updated.showPreview {
		t.Fatal("Space enabled a meaningless preview for empty results")
	}
}

func TestSearchResultsNavigationKeepsSelectionVisible(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(80, 14) // six visible result rows
	results := make([]SearchResult, 20)
	for i := range results {
		results[i] = SearchResult{FilePath: fmt.Sprintf("file-%02d.go", i), LineNumber: i + 1}
	}
	m.SetResults("file", "glob", results)

	for range 12 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.selectedIndex != 12 {
		t.Fatalf("selected index=%d, want 12", m.selectedIndex)
	}
	if m.selectedIndex < m.viewport.YOffset || m.selectedIndex >= m.viewport.YOffset+m.viewport.Height {
		t.Fatalf("selection %d is outside viewport [%d,%d)", m.selectedIndex, m.viewport.YOffset, m.viewport.YOffset+m.viewport.Height)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	if m.selectedIndex != 0 || m.viewport.YOffset != 0 {
		t.Fatalf("Home did not reveal first result: index=%d offset=%d", m.selectedIndex, m.viewport.YOffset)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.selectedIndex != len(results)-1 {
		t.Fatalf("End index=%d, want %d", m.selectedIndex, len(results)-1)
	}
}

func TestSearchResultsLongRowsKeepSelectedMarkerVisible(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(40, 14)
	results := make([]SearchResult, 10)
	for i := range results {
		results[i] = SearchResult{
			FilePath:   fmt.Sprintf("очень-длинный-каталог/%02d-%s.go", i, strings.Repeat("🙂имя", 12)),
			LineNumber: 120 + i,
			MatchCount: 3,
			Content:    strings.Repeat("long content ", 8),
		}
	}
	m.SetResults("long", "glob", results)

	for step := 0; step < 8; step++ {
		plain := stripAnsi(m.viewport.View())
		if !strings.Contains(plain, "> ") {
			t.Fatalf("selected marker disappeared at index=%d offset=%d:\n%s", m.selectedIndex, m.viewport.YOffset, plain)
		}
		if want := fmt.Sprintf(":%d", 120+m.selectedIndex); !strings.Contains(plain, want) {
			t.Fatalf("selected row lost line metadata %q at index=%d:\n%s", want, m.selectedIndex, plain)
		}
		if !strings.Contains(plain, ".go:") || !strings.Contains(plain, "(3 matches)") {
			t.Fatalf("selected row lost path tail or match count at index=%d:\n%s", m.selectedIndex, plain)
		}
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
}

func TestSearchResultsWideRowKeepsInlineContext(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(100, 18)
	m.SetResults("needle", "grep", []SearchResult{{
		FilePath: "internal/ui/search_results.go", LineNumber: 42, MatchCount: 2, Content: "matched content",
	}})
	plain := stripAnsi(m.viewport.View())
	for _, want := range []string{"search_results.go:42", "(2 matches)", "matched content"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("wide result row lost %q:\n%s", want, plain)
		}
	}
}

func TestSearchResultsRefreshClearsStalePreview(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(100, 24)
	m.SetResults("old", "grep", []SearchResult{{FilePath: "old.go", LineNumber: 7, Content: "old content"}})
	m.showPreview = true
	m.SetSize(100, 24)
	m.updatePreview()
	if !strings.Contains(stripAnsi(m.previewPane.View()), "old content") {
		t.Fatal("test setup did not populate preview")
	}

	m.SetResults("new", "grep", nil)
	if preview := stripAnsi(m.previewPane.View()); strings.Contains(preview, "old content") || m.previewVisible() {
		t.Fatalf("empty refresh retained stale preview: content=%q visible=%v", preview, m.previewVisible())
	}
	if m.selectedIndex != 0 || m.viewport.YOffset != 0 {
		t.Fatalf("empty refresh left stale navigation: index=%d offset=%d", m.selectedIndex, m.viewport.YOffset)
	}
}

func TestSearchResultsSanitizeDisplayWithoutChangingActionPath(t *testing.T) {
	rawPath := "путь/🙂\x1b]0;hijacked-title\aevil\nфайл.go"
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetResults("\x1b]0;query-title\aneedle\nquery", "\x1b[31mgrep\x1b[0m\nfake", []SearchResult{{
		FilePath:   rawPath,
		LineNumber: 7,
		Content:    "\x1b]52;c;clipboard\amatch\nforged row",
		Context:    []string{"\x1b]0;preview-title\afunc main()\nforged context"},
	}})
	m.showPreview = true
	m.SetSize(90, 24)
	m.updatePreview()

	view := m.View()
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b]") || strings.Contains(plain, "hijacked-title") || strings.Contains(plain, "clipboard") {
		t.Fatalf("search view retained terminal control payload: %q", view)
	}
	if !strings.Contains(plain, "🙂evil файл.go") {
		t.Fatalf("search view lost safe Unicode path content:\n%s", plain)
	}
	for _, forgedBreak := range []string{"evil\nфайл.go", "match\nforged row", "func main()\nforged context"} {
		if strings.Contains(plain, forgedBreak) {
			t.Fatalf("search view allowed a forged row via %q:\n%s", forgedBreak, plain)
		}
	}
	if m.query != "needle query" || m.tool != "grep fake" {
		t.Fatalf("metadata was not normalized: query=%q tool=%q", m.query, m.tool)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter did not emit an open action")
	}
	msg := cmd().(SearchResultsActionMsg)
	if msg.FilePath != rawPath || msg.LineNumber != 7 {
		t.Fatalf("display sanitization changed action payload: path=%q line=%d", msg.FilePath, msg.LineNumber)
	}
}

func TestSearchResultsSingleResultGrammarAndPreviewFallback(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetResults("main", "", []SearchResult{{FilePath: "main.go", LineNumber: 3, Content: "package main"}})
	m.showPreview = true
	m.SetSize(80, 24)
	m.updatePreview()

	view := stripAnsi(m.View())
	for _, want := range []string{"Search:", "(1 result)", "package main", "Hide preview"} {
		if !strings.Contains(view, want) {
			t.Fatalf("single-result view missing %q:\n%s", want, view)
		}
	}
}

func TestSearchResultsNarrowPreviewUsesOnePanelAndFits(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(40, 18)
	m.SetResults(strings.Repeat("needle", 10), "grep", []SearchResult{{
		FilePath:   "very/deeply/nested/path/to/a-long-filename.go",
		LineNumber: 12,
		Content:    "matched content",
	}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})

	if !m.previewVisible() {
		t.Fatal("preview did not open on a narrow terminal")
	}
	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"Esc/q Close", "Ctrl+j/k Scroll preview", "matched content"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("narrow preview missing %q:\n%s", want, plain)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 40 {
			t.Fatalf("narrow search line width = %d, want <= 40: %q", got, stripAnsi(line))
		}
	}
	if got := renderedLineCount(view); got > 18 {
		t.Fatalf("narrow search height = %d, want <= 18", got)
	}
	if boxes := strings.Count(plain, "╭"); boxes != 1 {
		t.Fatalf("narrow preview rendered %d panels, want one:\n%s", boxes, plain)
	}
}

func TestSearchResultsPreviewKeyboardScroll(t *testing.T) {
	contextLines := make([]string, 30)
	for i := range contextLines {
		contextLines[i] = fmt.Sprintf("context line %d", i)
	}
	m := NewSearchResultsModel(DefaultStyles())
	m.SetSize(80, 16)
	m.SetResults("context", "grep", []SearchResult{{FilePath: "main.go", LineNumber: 15, Context: contextLines}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.previewPane.YOffset == 0 {
		t.Fatal("Ctrl+j did not scroll the visible preview")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.previewPane.YOffset >= 3 {
		t.Fatalf("Ctrl+k did not scroll preview back up: offset=%d", m.previewPane.YOffset)
	}
}

func TestSearchResultsCopiesResultSnapshot(t *testing.T) {
	results := []SearchResult{{FilePath: "original.go", Content: "original", Context: []string{"before", "match"}}}
	m := NewSearchResultsModel(DefaultStyles())
	m.SetResults("original", "grep", results)

	results[0].FilePath = "mutated.go"
	results[0].Context[0] = "mutated"
	got, ok := m.GetSelectedResult()
	if !ok {
		t.Fatal("copied result disappeared")
	}
	if got.FilePath != "original.go" || !reflect.DeepEqual(got.Context, []string{"before", "match"}) {
		t.Fatalf("results changed through caller-owned data: %+v", got)
	}
}
