package ui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) renderPlanPauseBlock(msg PlanProgressMsg) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	if width < 4 {
		return truncateForWidth("Paused", width)
	}
	horizontalPadding := 1
	if width < 6 {
		horizontalPadding = 0
	}
	contentWidth := max(width-2-horizontalPadding*2, 1)
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorWarning).
		Padding(0, horizontalPadding)
	title := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("Plan paused")
	stepTitle := normalizeTimelineText(msg.CurrentTitle)
	if stepTitle == "" {
		stepTitle = "Untitled step"
	}
	what := fmt.Sprintf("Step %d · %s", msg.CurrentStepID, stepTitle)
	if msg.CurrentStepID <= 0 {
		what = "Current step · " + stepTitle
	}
	reason := normalizeTimelineText(msg.Reason)
	if reason == "" {
		reason = "execution watchdog or safety guard requested pause"
	}
	cmd := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true).Render("/resume-plan")
	content := fitPanelContent(title+"\n"+
		"What: "+what+"\n"+
		"Why: "+reason+"\n"+
		"Continue: "+cmd, contentWidth)
	return border.Width(width - 2).Render(content)
}
