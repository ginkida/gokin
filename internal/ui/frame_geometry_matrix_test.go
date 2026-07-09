package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestFrameGeometryAcrossStatesAndTerminalSizes(t *testing.T) {
	states := []State{
		StateInput,
		StateProcessing,
		StateStreaming,
		StateSettings,
		StateShortcutsOverlay,
		StateCommandPalette,
	}
	sizes := []struct {
		width  int
		height int
	}{
		{20, 8},
		{50, 12},
		{79, 16},
		{100, 24},
		{140, 32},
	}

	for _, size := range sizes {
		for _, state := range states {
			name := fmt.Sprintf("%dx%d_state_%d", size.width, size.height, state)
			t.Run(name, func(t *testing.T) {
				m := NewModel()
				m.workDir = "/home/test/github/gokin"
				m.currentModel = "glm-5.2"
				m.state = state
				m.applyResize(&tea.WindowSizeMsg{Width: size.width, Height: size.height})
				m.output.AppendText(strings.Repeat("output line\n", 40))

				view := m.View()
				if strings.Contains(stripAnsi(view), "2562047h") {
					t.Fatal("zero stream start rendered an absurd elapsed duration")
				}
				if got := lipgloss.Height(view); got != size.height {
					t.Fatalf("frame height = %d, want %d", got, size.height)
				}
				lines := strings.Split(stripAnsi(view), "\n")
				last := lines[len(lines)-1]
				wantStatus := stripAnsi(m.renderStatusBar())
				if last != wantStatus {
					t.Fatalf("last row is not status bar:\n got %q\nwant %q", last, wantStatus)
				}
				for i, line := range strings.Split(view, "\n") {
					if width := lipgloss.Width(line); width > size.width {
						t.Fatalf("row %d width = %d, terminal width = %d\n%s", i, width, size.width, stripAnsi(view))
					}
				}
			})
		}
	}
}
