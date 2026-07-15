package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Full-screen surfaces must obey Bubble Tea's geometry even while a terminal
// is being minimized one row or column at a time. This matrix intentionally
// exercises sizes too small for meaningful content: those frames should
// degrade, never wrap outside the terminal or panic.
func TestDegenerateFullscreenGeometryMatrix(t *testing.T) {
	setups := map[string]func(*Model){
		"input": func(m *Model) {
			m.state = StateInput
		},
		"permission": func(m *Model) {
			m.state = StatePermissionPrompt
			m.permRequest = &PermissionRequestMsg{ToolName: "bash", RiskLevel: "high"}
		},
		"file browser": func(m *Model) {
			m.state = StateFileBrowser
			m.fileBrowser.currentDir = "/repo"
			m.fileBrowser.entries = []FileEntry{{Name: "selected.go", Path: "/repo/selected.go"}}
		},
		"single diff": func(m *Model) {
			m.state = StateDiffPreview
			m.diffPreview.SetContent("界.go", "before", "after", "edit", false)
		},
		"multi diff": func(m *Model) {
			m.state = StateMultiDiffPreview
			m.multiDiffPreview.SetFiles([]DiffFile{{FilePath: "界.go", OldContent: "before", NewContent: "after"}})
		},
	}

	for name, setup := range setups {
		for width := 1; width <= 20; width++ {
			for height := 1; height <= 8; height++ {
				t.Run(fmt.Sprintf("%s/%dx%d", name, width, height), func(t *testing.T) {
					m := NewModel()
					setup(m)
					m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})

					view := m.View()
					if got := lipgloss.Height(view); got != height {
						t.Fatalf("frame height=%d, want %d:\n%s", got, height, stripAnsi(view))
					}
					for row, line := range strings.Split(view, "\n") {
						if got := lipgloss.Width(line); got > width {
							t.Fatalf("row=%d width=%d, want <=%d: %q", row, got, width, stripAnsi(line))
						}
					}
				})
			}
		}
	}
}
