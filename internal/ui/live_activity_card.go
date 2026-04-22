package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"gokin/internal/format"

	"github.com/charmbracelet/lipgloss"
)

func (m Model) shouldShowLiveActivityCard() bool {
	if m.isModalState() {
		return false
	}
	if m.state == StateProcessing || m.state == StateStreaming {
		return true
	}
	if len(m.backgroundTasks) > 0 {
		return true
	}
	return m.activityFeed != nil && m.activityFeed.HasActiveEntries()
}

func (m Model) renderLiveActivityCard() string {
	if !m.shouldShowLiveActivityCard() {
		return ""
	}

	width := m.width - 2
	if width < 48 {
		width = 48
	}
	innerWidth := width - 4
	if innerWidth < 36 {
		innerWidth = 36
	}

	accent := m.liveActivityAccentColor()
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().Foreground(accent).Bold(true)
	metaStyle := lipgloss.NewStyle().Foreground(ColorDim)
	labelStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	snapshot := ActivityFeedSnapshot{}
	if m.activityFeed != nil {
		snapshot = m.activityFeed.Snapshot(4, 2)
	}

	headerLeft := titleStyle.Render("Now")
	if chip := m.liveActivityStageChip(); chip != "" {
		headerLeft += " " + chip
	}
	headerRight := metaStyle.Render(m.liveActivityMeta())

	var content strings.Builder
	content.WriteString(joinLeftRight(headerLeft, headerRight, innerWidth))
	content.WriteString("\n")

	rows := []struct {
		label string
		value string
	}{
		{label: "Doing", value: m.liveActivityCurrentLine(snapshot)},
		{label: "Recent", value: m.liveActivityRecentLine(snapshot)},
		{label: "Next", value: m.liveActivityNextLine(snapshot)},
	}

	renderedRows := 0
	for _, row := range rows {
		if strings.TrimSpace(row.value) == "" {
			continue
		}
		content.WriteString(renderLiveActivityRow(row.label, row.value, innerWidth, labelStyle, valueStyle))
		content.WriteString("\n")
		renderedRows++
		if innerWidth < 72 && renderedRows >= 2 {
			break
		}
	}

	footer := m.liveActivityFooter(snapshot)
	if footer != "" {
		content.WriteString(footerStyle.Render(truncateRunes(footer, innerWidth)))
	}

	return borderStyle.Width(width).Render(strings.TrimRight(content.String(), "\n"))
}

func (m Model) liveActivityAccentColor() lipgloss.Color {
	switch {
	case m.retryAttempt > 0 || (!m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil)):
		return ColorWarning
	case m.streamIdleMsg != "":
		return ColorInfo
	case m.currentTool != "":
		return GetToolIconColor(m.currentTool)
	case m.state == StateStreaming:
		return ColorSuccess
	default:
		return ColorGradient2
	}
}

func (m Model) liveActivityStageChip() string {
	text := ""
	color := ColorMuted

	switch {
	case !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil):
		text = "RATE LIMIT"
		color = ColorWarning
	case m.retryAttempt > 0 && m.retryMax > 0:
		text = fmt.Sprintf("RETRY %d/%d", m.retryAttempt, m.retryMax)
		color = ColorWarning
	case m.currentTool != "":
		text = "RUNNING"
		color = GetToolIconColor(m.currentTool)
	case m.state == StateStreaming:
		text = "WRITING"
		color = ColorSuccess
	case m.state == StateProcessing:
		text = "WORKING"
		color = ColorSecondary
	default:
		return ""
	}

	return lipgloss.NewStyle().
		Foreground(color).
		Border(lipgloss.NormalBorder()).
		BorderForeground(color).
		Padding(0, 1).
		Bold(true).
		Render(text)
}

func (m Model) liveActivityMeta() string {
	var parts []string
	if provider := strings.TrimSpace(m.runtimeStatus.Provider); provider != "" {
		parts = append(parts, provider)
	}
	if model := strings.TrimSpace(m.currentModel); model != "" {
		parts = append(parts, shortenModelName(model))
	}
	if m.planProgressMode && m.planProgress != nil && m.planProgress.TotalSteps > 0 {
		parts = append(parts, fmt.Sprintf("step %d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps))
	}
	if len(parts) == 0 {
		return "session active"
	}
	return strings.Join(parts, " · ")
}

func (m Model) liveActivityCurrentLine(snapshot ActivityFeedSnapshot) string {
	if m.currentTool != "" {
		title := capitalizeToolName(m.currentTool)
		if active := len(m.activeToolCalls); active > 1 {
			title = fmt.Sprintf("%s · %d tools in flight", title, active)
		}
		if m.currentToolInfo != "" {
			title += " — " + m.currentToolInfo
		}
		if !m.toolStartTime.IsZero() {
			title += " · " + format.Duration(time.Since(m.toolStartTime))
		}
		return title
	}

	if m.state == StateStreaming {
		switch {
		case m.lastStreamSnippet() != "":
			return "Writing response — " + m.lastStreamSnippet()
		case m.responseToolCount > 0:
			return fmt.Sprintf("Writing response after %d tool calls", m.responseToolCount)
		default:
			return "Writing response"
		}
	}

	if label := strings.TrimSpace(m.processingLabel); label != "" {
		return label
	}

	if len(m.backgroundTasks) > 0 {
		return m.formatBackgroundTaskStatus(len(m.backgroundTasks))
	}

	if snapshot.RunningAgents > 0 || snapshot.RunningTools > 0 {
		return fmt.Sprintf("%d agents · %d tools active", snapshot.RunningAgents, snapshot.RunningTools)
	}

	return "Waiting for next action"
}

func (m Model) liveActivityRecentLine(snapshot ActivityFeedSnapshot) string {
	if len(snapshot.RecentLog) > 0 {
		return snapshot.RecentLog[len(snapshot.RecentLog)-1]
	}
	if m.responseToolCount > 0 {
		return fmt.Sprintf("%d tool calls completed in this response", m.responseToolCount)
	}
	if len(m.agentRecentTools) > 0 {
		start := 0
		if len(m.agentRecentTools) > 3 {
			start = len(m.agentRecentTools) - 3
		}
		return strings.Join(m.agentRecentTools[start:], " → ")
	}
	return ""
}

func (m Model) liveActivityNextLine(snapshot ActivityFeedSnapshot) string {
	switch {
	case !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil):
		return fmt.Sprintf("Auto-retry scheduled in %s", format.Duration(time.Until(m.rateLimitWaitUntil)))
	case m.retryAttempt > 0 && m.retryMax > 0:
		return fmt.Sprintf("Provider recovery in progress (%d/%d)", m.retryAttempt, m.retryMax)
	case m.streamIdleMsg != "":
		return m.streamIdleMsg
	}

	if m.planProgressMode && m.planProgress != nil {
		title := strings.TrimSpace(m.planProgress.CurrentTitle)
		if title != "" {
			return title
		}
		return fmt.Sprintf("Plan step %d of %d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
	}

	if snapshot.RunningTools > 1 {
		return fmt.Sprintf("%d tool calls are still running in parallel", snapshot.RunningTools)
	}
	if snapshot.RunningAgents > 0 {
		return fmt.Sprintf("%d background agents are still working", snapshot.RunningAgents)
	}
	if label := strings.TrimSpace(m.processingLabel); label != "" && !strings.EqualFold(label, "thinking") {
		return label
	}
	if m.state == StateStreaming {
		return "Watching for the next tool call or final answer"
	}
	return ""
}

func (m Model) liveActivityFooter(snapshot ActivityFeedSnapshot) string {
	var hints []string
	if m.state == StateProcessing || m.state == StateStreaming {
		hints = append(hints, "Esc cancel")
	}
	if m.activityFeed != nil && !m.activityFeed.IsVisible() && (snapshot.RunningAgents > 0 || snapshot.RunningTools > 1) {
		hints = append(hints, "Ctrl+O live activity")
	}
	if m.lastToolOutputIndex >= 0 {
		hints = append(hints, "e last tool output")
	}
	if len(hints) == 0 {
		return ""
	}
	return strings.Join(hints, " • ")
}

func renderLiveActivityRow(label, value string, width int, labelStyle, valueStyle lipgloss.Style) string {
	labelText := fmt.Sprintf("%-6s", label)
	maxValue := width - utf8.RuneCountInString(labelText) - 1
	if maxValue < 12 {
		maxValue = 12
	}
	return labelStyle.Render(labelText) + " " + valueStyle.Render(truncateRunes(value, maxValue))
}

func joinLeftRight(left, right string, width int) string {
	if right == "" {
		return truncateRunes(left, width)
	}
	padding := width - lipgloss.Width(left) - lipgloss.Width(right)
	if padding < 1 {
		return truncateRunes(left+" · "+right, width)
	}
	return left + strings.Repeat(" ", padding) + right
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}
