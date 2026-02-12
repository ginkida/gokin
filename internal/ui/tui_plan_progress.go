package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderPlanProgress renders plan progress information for status bar.
func renderPlanProgress(planProgress *PlanProgressMsg, width int, mutedStyle lipgloss.Style) string {
	if planProgress == nil {
		return ""
	}

	progressIcon := "→"
	if planProgress.Status == "completed" {
		progressIcon = "✓"
	} else if planProgress.Status == "failed" {
		progressIcon = "✗"
	}

	progressText := fmt.Sprintf("%s %d/%d (%.0f%%)",
		progressIcon,
		planProgress.Completed,
		planProgress.TotalSteps,
		planProgress.Progress*100)

	// Show current step title if space permits
	if width >= 100 && planProgress.CurrentTitle != "" {
		title := planProgress.CurrentTitle
		if len(title) > 20 {
			title = title[:17] + "..."
		}
		progressText += fmt.Sprintf(" • %s", title)
	}

	// Show sub-agent progress if available
	if planProgress.SubStepInfo != "" && width >= 120 {
		info := planProgress.SubStepInfo
		if len(info) > 30 {
			info = info[:27] + "..."
		}
		progressText += fmt.Sprintf(" [%s]", info)
	}

	return lipgloss.NewStyle().Foreground(ColorPlan).Render("⚡ ") + mutedStyle.Render(progressText)
}

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
