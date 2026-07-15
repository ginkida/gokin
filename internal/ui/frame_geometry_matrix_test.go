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
				last := strings.TrimRight(lines[len(lines)-1], " ")
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

// TestFrameGeometry_ShortContentKeepsInputAtBottom pins the v0.100.70 field
// regression: with SHORT output content (a fresh conversation) the styled
// viewport rendered one row MORE than requested, View's old shrink loop
// "corrected" by walking the output budget down to ZERO, the output vanished,
// and the input box floated to the TOP of the screen with all the slack
// inserted before the status bar. The original geometry matrix always filled
// the viewport with 40 lines, so the short-content shape was never covered.
// ViewWithHeight now guarantees exactly the requested rows (exactRows) and
// the shrink loop is gone.
func TestFrameGeometry_ShortContentKeepsInputAtBottom(t *testing.T) {
	for _, fill := range []struct {
		name  string
		lines int
	}{
		{"short_3_lines", 3},
		{"medium_10_lines", 10},
		{"long_40_lines", 40},
	} {
		t.Run(fill.name, func(t *testing.T) {
			m := NewModel()
			m.workDir = "/home/test/github/gokin"
			m.currentModel = "glm-5.2"
			m.state = StateInput
			m.applyResize(&tea.WindowSizeMsg{Width: 100, Height: 30})
			for i := 0; i < fill.lines; i++ {
				m.output.AppendText(fmt.Sprintf("output line %d\n", i))
			}

			view := stripAnsi(m.View())
			lines := strings.Split(view, "\n")

			if got := lipgloss.Height(view); got != 30 {
				t.Fatalf("frame height = %d, want 30", got)
			}

			// The output content must be VISIBLE (the shrink-loop bug blanked
			// it entirely).
			lastContent := fmt.Sprintf("output line %d", fill.lines-1)
			if !strings.Contains(view, lastContent) {
				t.Fatalf("output content %q missing from the frame:\n%s", lastContent, view)
			}

			// The input prompt must sit in the BOTTOM quarter of the frame,
			// right above the status bar — never float to the top.
			inputRow := -1
			for i, line := range lines {
				if strings.Contains(line, "›") {
					inputRow = i
				}
			}
			if inputRow == -1 {
				t.Fatalf("input prompt not found in frame:\n%s", view)
			}
			if inputRow < len(lines)*3/4 {
				t.Fatalf("input prompt floated up: row %d of %d (must be in the bottom quarter)\n%s",
					inputRow, len(lines), view)
			}
		})
	}
}

// TestFrameGeometry_NoGapBetweenInputAndStatusBar pins the follow-up to the
// v0.100.71 hotfix: the input box was back at the bottom, but the compositor
// still reserved a separator row AND double-counted the marker row, leaving
// 2-3 blank rows wedged between the input's bottom border and the status bar.
// The slack belongs ABOVE the input (inside the output region), with the
// footer hugging the terminal's last rows — the pre-v0.100.70 look.
func TestFrameGeometry_NoGapBetweenInputAndStatusBar(t *testing.T) {
	for _, fill := range []struct {
		name  string
		lines int
	}{
		{"short_3_lines", 3},
		{"long_40_lines", 40},
	} {
		t.Run(fill.name, func(t *testing.T) {
			m := NewModel()
			m.workDir = "/home/test/github/gokin"
			m.currentModel = "glm-5.2"
			m.state = StateInput
			m.applyResize(&tea.WindowSizeMsg{Width: 100, Height: 30})
			for i := 0; i < fill.lines; i++ {
				m.output.AppendText(fmt.Sprintf("output line %d\n", i))
			}

			view := stripAnsi(m.View())
			lines := strings.Split(view, "\n")
			if got := len(lines); got != 30 {
				t.Fatalf("frame rows = %d, want 30", got)
			}

			// Find the input box's bottom border (the ╰ row).
			bottomBorderRow := -1
			for i, line := range lines {
				if strings.Contains(line, "╰") {
					bottomBorderRow = i
				}
			}
			if bottomBorderRow == -1 {
				t.Fatalf("input bottom border not found:\n%s", view)
			}

			// Count fully blank rows between the border and the status bar
			// (the last row). At most ONE is tolerable; 2+ is the wedge.
			blank := 0
			for i := bottomBorderRow + 1; i < len(lines)-1; i++ {
				if strings.TrimSpace(lines[i]) == "" {
					blank++
				}
			}
			if blank > 1 {
				t.Fatalf("%d blank rows wedged between the input and the status bar (want <= 1):\n%s",
					blank, view)
			}
		})
	}
}
