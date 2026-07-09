package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestStatusBarRemainsOnTerminalBottomRow(t *testing.T) {
	m := NewModel()
	m.workDir = "/home/test/github/gokin"
	m.currentModel = "glm-5.2"
	m.permissionsEnabled = false
	m.sandboxEnabled = false
	m.showTokens = true
	m.tokenUsage = &TokenUsageMsg{
		Tokens:      44_500,
		MaxTokens:   1_000_000,
		PercentUsed: 0.0445,
	}
	m.responseToolCount = 82
	m.responseToolFailures = 3
	m.applyResize(&tea.WindowSizeMsg{Width: 140, Height: 24})
	m.output.AppendText(strings.Repeat("streamed output line\n", 80))

	states := []struct {
		name  string
		apply func()
	}{
		{
			name: "idle",
			apply: func() {
				m.state = StateInput
				m.currentTool = ""
				m.processingLabel = ""
			},
		},
		{
			name: "processing",
			apply: func() {
				m.state = StateProcessing
				m.processingLabel = "Analyzing"
			},
		},
		{
			name: "tool",
			apply: func() {
				m.state = StateProcessing
				m.processingLabel = ""
				m.currentTool = "write"
				m.currentToolInfo = "internal/ui/tui.go"
			},
		},
		{
			name: "multiline input",
			apply: func() {
				m.state = StateInput
				m.currentTool = ""
				m.input.textarea.SetValue("first line\nsecond line\nthird line")
				m.input.InsertNewline()
			},
		},
	}

	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			tc.apply()
			view := m.View()
			if got := lipgloss.Height(view); got != m.height {
				t.Fatalf("frame height = %d, want terminal height %d", got, m.height)
			}
			lines := strings.Split(stripAnsi(view), "\n")
			last := lines[len(lines)-1]
			if !strings.Contains(last, "glm-5.2") || !strings.Contains(last, "44.5K/1.0M") {
				t.Fatalf("last terminal row is not the status bar: %q", last)
			}
		})
	}

	m.applyResize(&tea.WindowSizeMsg{Width: 100, Height: 8})
	m.state = StateInput
	m.input.textarea.SetValue("one\ntwo\nthree\nfour\nfive\nsix")
	for i := 1; i < 6; i++ {
		m.input.InsertNewline()
	}
	shortView := m.View()
	if got := lipgloss.Height(shortView); got != m.height {
		t.Fatalf("short-terminal frame height = %d, want %d", got, m.height)
	}
	shortLines := strings.Split(stripAnsi(shortView), "\n")
	if !strings.Contains(shortLines[len(shortLines)-1], "glm-5.2") {
		t.Fatalf("short-terminal status bar is not on last row: %q", shortLines[len(shortLines)-1])
	}
}
