package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"gokin/internal/format"
)

func (m *Model) renderTaskTimelineStart(taskIndex int, planType, message string) string {
	headStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	typeStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	bodyStyle := lipgloss.NewStyle().Foreground(ColorText)

	var out strings.Builder
	out.WriteString(headStyle.Render(fmt.Sprintf("  ◌ Subtask %d", taskIndex)))
	if label := timelineLabel(planType); label != "" {
		out.WriteString(" ")
		out.WriteString(typeStyle.Render(label))
	}

	if summary := normalizeTimelineText(message); summary != "" {
		out.WriteString("\n")
		out.WriteString(bodyStyle.Render("    " + summary))
	}

	return out.String()
}

func (m *Model) renderTaskTimelineProgress(progress float64, message string) string {
	connectorStyle := lipgloss.NewStyle().Foreground(ColorDim)
	progressStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	bodyStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	var out strings.Builder
	out.WriteString(connectorStyle.Render("    ↳"))

	if progress >= 0 {
		out.WriteString(" ")
		out.WriteString(progressStyle.Render(renderMiniTimelineBar(progress, 10)))
		out.WriteString(" ")
		out.WriteString(progressStyle.Render(fmt.Sprintf("%d%%", int(progress*100))))
	}

	if summary := normalizeTimelineText(message); summary != "" {
		out.WriteString(" ")
		out.WriteString(bodyStyle.Render(summary))
	}

	return out.String()
}

func (m *Model) renderTaskTimelineDone(success bool, duration time.Duration, err error) string {
	var (
		iconStyle   lipgloss.Style
		textStyle   lipgloss.Style
		detailStyle lipgloss.Style
		icon        string
		statusText  string
	)

	if success {
		iconStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
		textStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
		detailStyle = lipgloss.NewStyle().Foreground(ColorDim)
		icon = "✓"
		statusText = "Completed"
	} else {
		iconStyle = lipgloss.NewStyle().Foreground(ColorError)
		textStyle = lipgloss.NewStyle().Foreground(ColorError)
		detailStyle = lipgloss.NewStyle().Foreground(ColorRose)
		icon = "✗"
		statusText = "Failed"
	}

	var out strings.Builder
	out.WriteString("    ")
	out.WriteString(iconStyle.Render(icon))
	out.WriteString(" ")
	out.WriteString(textStyle.Render(statusText))

	if duration > 0 {
		out.WriteString(" ")
		out.WriteString(detailStyle.Render(format.Duration(duration)))
	}

	if err != nil {
		out.WriteString(" ")
		out.WriteString(detailStyle.Render("· " + normalizeTimelineText(err.Error())))
	}

	return out.String()
}

func (m *Model) renderAgentTimelineStart(agentType, description string) string {
	headStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	bodyStyle := lipgloss.NewStyle().Foreground(ColorText)
	typeLabel := timelineLabel(agentType)

	var out strings.Builder
	out.WriteString(headStyle.Render("  ◌ Agent"))
	if typeLabel != "" {
		out.WriteString(" ")
		out.WriteString(headStyle.Render(typeLabel))
	}
	out.WriteString(headStyle.Render(" started"))

	if summary := normalizeTimelineText(description); summary != "" {
		out.WriteString("\n")
		out.WriteString(bodyStyle.Render("    " + summary))
	}

	return out.String()
}

func renderMiniTimelineBar(progress float64, width int) string {
	if width < 1 {
		width = 1
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 1 {
		progress = 1
	}

	filled := int(progress * float64(width))
	if filled > width {
		filled = width
	}

	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func timelineProgressBucket(progress float64) int {
	if progress < 0 {
		return -1
	}
	if progress > 1 {
		progress = 1
	}
	return int(progress * 10)
}

func timelineLabel(kind string) string {
	kind = normalizeTimelineText(kind)
	if kind == "" {
		return ""
	}
	return capitalizeToolName(strings.ReplaceAll(kind, " ", "_"))
}

func normalizeTimelineText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}
