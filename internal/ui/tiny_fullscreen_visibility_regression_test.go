package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestSubminimumWindowDoesNotRenderOutsideAnnouncedGeometry(t *testing.T) {
	for _, size := range []struct{ width, height int }{{9, 5}, {5, 3}, {2, 2}, {1, 1}, {0, 0}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := NewModel()
			m.state = StateInput
			m.applyResize(&tea.WindowSizeMsg{Width: size.width, Height: size.height})

			view := m.View()
			if size.width == 0 || size.height == 0 {
				if view != "" {
					t.Fatalf("announced %dx%d window rendered content: %q", size.width, size.height, stripAnsi(view))
				}
				return
			}
			if got := lipgloss.Height(view); got > size.height {
				t.Fatalf("announced %dx%d window rendered %d rows:\n%s", size.width, size.height, got, stripAnsi(view))
			}
			for row, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > size.width {
					t.Fatalf("announced %dx%d window row=%d rendered %d cells: %q", size.width, size.height, row, got, stripAnsi(line))
				}
			}
		})
	}
}

func TestTinyPermissionFrameKeepsSelectedDecisionAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		width int
		want  string
	}{
		{width: 20, want: "> 1. Allow once"},
		{width: 10, want: "> 1."},
	} {
		m := NewModel()
		m.state = StatePermissionPrompt
		m.permSelectedOption = int(PermissionAllow)
		m.permRequest = &PermissionRequestMsg{
			ToolName:  "bash",
			RiskLevel: "high",
			Reason:    "Run the requested command",
		}
		m.applyResize(&tea.WindowSizeMsg{Width: tc.width, Height: 6})

		view := m.View()
		plain := stripAnsi(view)
		for _, want := range []string{tc.want, "Esc Deny"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("width=%d tiny permission frame hid %q:\n%s", tc.width, want, plain)
			}
		}
		assertTinyFrameFits(t, view, tc.width, 6)
	}
}

func TestTinyFileBrowserFrameKeepsSelectionAndRecovery(t *testing.T) {
	m := NewModel()
	m.state = StateFileBrowser
	m.fileBrowser.currentDir = "/repo"
	m.fileBrowser.entries = []FileEntry{{
		Name: "selected.go",
		Path: "/repo/selected.go",
	}}
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 6})

	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"selected.go", "Esc/q Close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("tiny file-browser frame hid %q:\n%s", want, plain)
		}
	}
	assertTinyFrameFits(t, view, 20, 6)
}

func TestTinyDiffFramesKeepReviewedFileIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Model)
	}{
		{
			name: "single",
			setup: func(m *Model) {
				m.state = StateDiffPreview
				m.diffPreview.SetContent("界.go", "before", "after", "edit", false)
			},
		},
		{
			name: "multi",
			setup: func(m *Model) {
				m.state = StateMultiDiffPreview
				m.multiDiffPreview.SetFiles([]DiffFile{{
					FilePath:   "界.go",
					OldContent: "before",
					NewContent: "after",
				}})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			tc.setup(m)
			m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 6})

			view := m.View()
			plain := stripAnsi(view)
			for _, want := range []string{"界.go", "y Apply", "n Reject"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("tiny %s diff frame hid %q:\n%s", tc.name, want, plain)
				}
			}
			assertTinyFrameFits(t, view, 20, 6)
		})
	}
}

func assertTinyFrameFits(t *testing.T, view string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got != height {
		t.Fatalf("tiny frame height=%d, want %d:\n%s", got, height, stripAnsi(view))
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("tiny frame row=%d width=%d, want <=%d: %q", row, got, width, stripAnsi(line))
		}
	}
}
