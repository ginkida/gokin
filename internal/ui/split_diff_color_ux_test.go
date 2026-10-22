package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestColoredSplitDiffMarkersRemainVisibleChanges(t *testing.T) {
	m := NewDiffPreviewModel(DefaultStyles())
	colored := "#    OLD | #    NEW | Δ\n1    old | 1    new | \x1b[38;2;200;100;50m≠\x1b[0m\n"
	if offsets := m.detectChangeOffsets(colored); len(offsets) != 1 || offsets[0] != 1 {
		t.Fatalf("colored split marker lost: %v", offsets)
	}
	unchanged := "1    code | + literal | 1    code | + literal |  \n"
	if offsets := m.detectChangeOffsets(unchanged); len(offsets) != 0 {
		t.Fatalf("literal code text mistaken for a change marker: %v", offsets)
	}
}

func TestSplitDiffFitsTerminalWithWideCharactersAndManyLineNumbers(t *testing.T) {
	for _, width := range []int{84, 100, 120} {
		m := NewDiffPreviewModel(DefaultStyles())
		m.SetSize(width, 24)
		old := strings.Repeat("漢字🎉 sample text\n", 10005)
		m.SetContent("sample.go", old, old+"added\n", "edit", false)
		for i, line := range strings.Split(m.renderSideBySide(), "\n") {
			if lipgloss.Width(line) > m.viewport.Width {
				t.Fatalf("width %d row %d exceeds viewport: %d > %d", width, i, lipgloss.Width(line), m.viewport.Width)
			}
		}
		if !m.hasVisibleChanges() || strings.Contains(stripAnsi(m.View()), "No content changes detected") {
			t.Fatal("real edit was hidden by split rendering")
		}
	}
}
