package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)


func (m Model) renderPlanPauseBlock(msg PlanProgressMsg) string {
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(0, 1)
	title := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("Plan paused")
	what := fmt.Sprintf("Step %d: %s", msg.CurrentStepID, msg.CurrentTitle)
	if msg.CurrentStepID <= 0 {
		what = "Step unknown"
	}
	reason := msg.Reason
	if strings.TrimSpace(reason) == "" {
		reason = "execution watchdog or safety guard requested pause"
	}
	cmd := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true).Render("/resume-plan")
	content := title + "\n" +
		"What: " + what + "\n" +
		"Why: " + reason + "\n" +
		"Continue: " + cmd
	return border.Render(content)
}
