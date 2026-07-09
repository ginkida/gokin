package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestStatusBarNeverWrapsPastTerminalWidth(t *testing.T) {
	for _, width := range []int{10, 20, 40, 59, 60, 79, 80, 119, 120, 160} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := NewModel()
			m.width = width
			m.workDir = "/home/test/extremely/long/nested/project/path/that/would/normally/overflow"
			m.currentModel = "glm-5.2-with-an-extraordinarily-long-preview-suffix"
			m.permissionsEnabled = false
			m.sandboxEnabled = false
			m.showTokens = true
			m.tokenUsage = &TokenUsageMsg{
				Tokens:      987_654,
				MaxTokens:   1_000_000,
				PercentUsed: 0.987654,
				IsEstimate:  true,
			}
			m.state = StateStreaming
			m.responseToolCount = 12_345
			m.responseToolFailures = 999
			m.mcpTotal = 99
			m.mcpHealthy = 0

			got := m.renderStatusBar()
			if strings.Contains(got, "\n") {
				t.Fatalf("status bar contains a newline at width %d: %q", width, stripAnsi(got))
			}
			if gotWidth := lipgloss.Width(got); gotWidth > width {
				t.Fatalf("status width = %d, terminal width = %d: %q", gotWidth, width, stripAnsi(got))
			}
		})
	}
}

func TestCompactStatusPreservesEstimateMarker(t *testing.T) {
	m := NewModel()
	m.width = 70
	m.showTokens = true
	m.tokenUsage = &TokenUsageMsg{
		Tokens:      44_500,
		MaxTokens:   1_000_000,
		PercentUsed: 0.0445,
		IsEstimate:  true,
	}
	if got := stripAnsi(m.renderStatusBar()); !strings.Contains(got, "≈44.5K/1.0M") {
		t.Fatalf("compact estimated usage lost ≈ marker: %q", got)
	}
}
