package ui

import (
	"fmt"
	"math"
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
	out.WriteString(headStyle.Render(fmt.Sprintf("  ◌ Subtask %d", max(taskIndex, 1))))
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

	normalizedProgress, determinate := normalizeTimelineProgress(progress)
	if determinate {
		out.WriteString(" ")
		out.WriteString(progressStyle.Render(renderMiniTimelineBar(normalizedProgress, 10)))
		out.WriteString(" ")
		out.WriteString(progressStyle.Render(fmt.Sprintf("%d%%", int(math.Round(normalizedProgress*100)))))
	}

	if summary := normalizeTimelineText(message); summary != "" {
		out.WriteString(" ")
		out.WriteString(bodyStyle.Render(summary))
	} else if !determinate {
		out.WriteString(" ")
		out.WriteString(bodyStyle.Render("Working…"))
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

	if errText := normalizedTimelineError(err); errText != "" {
		out.WriteString(" ")
		out.WriteString(detailStyle.Render("· " + errText))
	} else if !success {
		out.WriteString(" ")
		out.WriteString(detailStyle.Render("· No error details"))
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
	progress, _ = normalizeTimelineProgress(progress)

	filled := int(progress * float64(width))
	if filled > width {
		filled = width
	}

	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func timelineProgressBucket(progress float64) int {
	progress, determinate := normalizeTimelineProgress(progress)
	if !determinate {
		return -1
	}
	return int(progress * 10)
}

func normalizeTimelineProgress(progress float64) (float64, bool) {
	if math.IsNaN(progress) || math.IsInf(progress, 0) || progress < 0 {
		return 0, false
	}
	return min(progress, 1), true
}

func normalizedTimelineError(err error) string {
	if err == nil {
		return ""
	}
	return normalizeTimelineText(err.Error())
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
