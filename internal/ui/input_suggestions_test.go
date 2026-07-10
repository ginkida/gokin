package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// renderSuggestions must bound every line to the input width so long command
// descriptions / usage / file paths can't overflow a narrow terminal — the
// suggestion box has no fixed width and grows to its widest line.
func TestRenderSuggestions_CommandsTruncateToWidth(t *testing.T) {
	const term = 44
	m := NewInputModel(DefaultStyles(), "/tmp")
	m.SetWidth(term)
	m.suggestionType = SuggestionCommand
	m.suggestions = []CommandInfo{
		{
			Name:        "status",
			Description: strings.Repeat("a very long description that would overflow ", 6),
			Usage:       strings.Repeat("/status [a] [b] [c] [d] ", 6),
		},
		{Name: "x", Description: "short"},
	}
	m.suggestionIndex = 0

	assertNoLineExceeds(t, m.renderSuggestions(), term)
}

func TestRenderSuggestions_FilePathsTruncateToWidth(t *testing.T) {
	const term = 44
	m := NewInputModel(DefaultStyles(), "/tmp")
	m.SetWidth(term)
	m.suggestionType = SuggestionFile
	m.fileSuggestions = []string{
		"/tmp/" + strings.Repeat("deep/", 12) + "verylongfilename.go",
		"/tmp/short.go",
	}
	m.suggestionIndex = 0

	assertNoLineExceeds(t, m.renderSuggestions(), term)
}

func TestUpdateFileSuggestions_QuotedPrefix(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "space file.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewInputModel(DefaultStyles(), work)
	m.updateFileSuggestions(`/open "space`)
	if !m.showSuggestions {
		t.Fatal("quoted file prefix should show suggestions")
	}
	if len(m.fileSuggestions) != 1 || filepath.Base(m.fileSuggestions[0]) != "space file.go" {
		t.Fatalf("fileSuggestions = %v, want space file.go", m.fileSuggestions)
	}
	if m.ghostText != " file.go" {
		t.Fatalf("ghostText = %q, want %q", m.ghostText, " file.go")
	}
}

func TestUpdateGhostText_FileQuotedPrefix(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "space file.go")

	m := NewInputModel(DefaultStyles(), work)
	m.suggestionType = SuggestionFile
	m.fileSuggestions = []string{path}

	m.updateGhostText(`/open "space`)
	if m.ghostText != " file.go" {
		t.Fatalf("ghostText = %q, want %q", m.ghostText, " file.go")
	}
}

func assertNoLineExceeds(t *testing.T, rendered string, max int) {
	t.Helper()
	for _, line := range strings.Split(stripAnsi(rendered), "\n") {
		if w := lipgloss.Width(line); w > max {
			t.Errorf("suggestion line overflows width %d (%d cols): %q", max, w, line)
		}
	}
}
