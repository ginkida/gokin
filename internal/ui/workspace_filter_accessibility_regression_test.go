package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestFileBrowserFilterBackspaceDeletesOneVisibleCharacter(t *testing.T) {
	for _, cluster := range []string{"👍🏽", "e\u0301", "👩‍💻"} {
		t.Run(cluster, func(t *testing.T) {
			m := NewFileBrowserModel(DefaultStyles())
			if err := m.SetPath(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			m.filterActive = true
			m.filterInput = "find " + cluster
			m.filter = m.filterInput

			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
			if updated.filterInput != "find " || updated.filter != "find " {
				t.Fatalf("Backspace split %q: filter=%q input=%q", cluster, updated.filter, updated.filterInput)
			}
		})
	}
}

func TestTruncateTailForWidthPreservesWholeGrapheme(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  string
	}{
		{name: "emoji without ellipsis", value: "prefix👍🏽", width: 2, want: "👍🏽"},
		{name: "emoji with ellipsis", value: "prefix👍🏽", width: 3, want: "…👍🏽"},
		{name: "combining mark without ellipsis", value: "prefixe\u0301", width: 1, want: "e\u0301"},
		{name: "combining mark with ellipsis", value: "prefixe\u0301", width: 2, want: "…e\u0301"},
		{name: "ZWJ emoji without ellipsis", value: "prefix👩‍💻", width: 2, want: "👩‍💻"},
		{name: "ZWJ emoji with ellipsis", value: "prefix👩‍💻", width: 3, want: "…👩‍💻"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateTailForWidth(tt.value, tt.width)
			if got != tt.want {
				t.Fatalf("truncateTailForWidth(%q, %d) = %q, want %q", tt.value, tt.width, got, tt.want)
			}
			if gotWidth := lipgloss.Width(got); gotWidth > tt.width {
				t.Fatalf("result width=%d, want <=%d: %q", gotWidth, tt.width, got)
			}
		})
	}
}

func TestTinyFileBrowserFilterKeepsEditingTailAndCursorVisible(t *testing.T) {
	m := NewFileBrowserModel(DefaultStyles())
	m.SetSize(20, 4)
	if err := m.SetPath(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	m.filterActive = true
	m.filterInput = strings.Repeat("界", 20) + "needle"
	m.filter = m.filterInput

	view := m.View()
	plain := renderToPlain(view)
	for _, want := range []string{"needle▊", "Enter/Esc", "Done"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("tiny active filter lost %q:\n%s", want, plain)
		}
	}
	for row, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 20 {
			t.Fatalf("row %d width=%d, want <=20: %q", row, width, renderToPlain(line))
		}
	}
}
