package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMinimalStatusShowsFrozenOffBottomTranscript(t *testing.T) {
	for _, width := range []int{10, 20, 40} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			m := NewModel()
			m.state = StateStreaming
			m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 8})
			for i := range 30 {
				m.output.AppendLine(fmt.Sprintf("line %02d", i))
			}
			m.output.viewport.GotoBottom()
			m.output.SetFrozen(false)

			_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyPgUp})
			if m.output.IsAtBottom() || !m.output.IsFrozen() {
				t.Fatalf("setup did not leave transcript frozen above bottom: bottom=%v frozen=%v",
					m.output.IsAtBottom(), m.output.IsFrozen())
			}

			status := stripAnsi(m.renderStatusBar())
			if !strings.Contains(status, "↑") || !strings.Contains(status, "%") {
				t.Fatalf("minimal status hid frozen off-bottom transcript at width %d: %q", width, status)
			}
		})
	}
}

func TestNoOpScrollUpAtBottomKeepsFollowingStream(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "Ctrl+B", key: tea.KeyMsg{Type: tea.KeyCtrlB}},
		{name: "Ctrl+U", key: tea.KeyMsg{Type: tea.KeyCtrlU}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateStreaming
			m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 8})
			m.SetReducedMotion(true)
			m.output.AppendLine("head")
			m.output.viewport.GotoBottom()
			m.output.SetFrozen(false)

			beforeOffset := m.output.viewport.YOffset
			_ = m.handleGlobalKeys(tt.key)
			if got := m.output.viewport.YOffset; got != beforeOffset {
				t.Errorf("no-op %s moved viewport from %d to %d", tt.name, beforeOffset, got)
			}
			if m.output.IsFrozen() {
				t.Errorf("no-op %s at bottom froze auto-follow", tt.name)
			}

			for i := range 12 {
				m.output.AppendLine(fmt.Sprintf("new %02d", i))
			}
			m.output.AppendLine("TAIL")
			view := stripAnsi(m.View())
			if !strings.Contains(view, "TAIL") {
				t.Errorf("stream tail disappeared after no-op %s:\n%s", tt.name, view)
			}
		})
	}
}
