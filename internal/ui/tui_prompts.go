package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderPromptFooterLine keeps the safety/recovery action on one physical row.
// Prompt containers are height-cropped by the frame compositor; allowing a
// Width-styled footer to wrap can split "Esc Cancel" across rows or crop it
// entirely. Callers put recovery first, then lower-priority actions.
func renderPromptFooterLine(style lipgloss.Style, width int, text string) string {
	return style.Render(truncateForWidth(text, max(width, 1)))
}

func isUnavailablePromptNotice(text string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "unavailable:")
}

// promptPaletteWidth returns the inner content width for prompt cards and
// whether the card should render with a rounded border + fixed width.
// On narrow terminals (width < minBorderedPromptWidth) we skip the border
// entirely and let the content flow at the terminal's natural width — a
// 45-cell bordered container in a 40-cell terminal overflows and breaks
// the layout horizontally, which is worse than a borderless prompt.
func promptPaletteWidth(termWidth int) (width int, bordered bool) {
	if termWidth <= 0 {
		termWidth = 80
	}
	if termWidth < minBorderedPromptWidth {
		// Fall back: no border, use the actual available width. A historical
		// 30-column floor made 20-column tmux/SSH panes overflow by design.
		w := termWidth - 4 // 2-space left padding + 2-space right margin
		if w < 1 {
			w = 1
		}
		return w, false
	}
	w := min(78, termWidth-6)
	return max(w, 1), true
}

// minBorderedPromptWidth is the threshold below which prompt cards drop
// the rounded border and fixed Width(). Set to give the 45-wide content
// + 2 border cells + 2 padding cells + a small right margin room to
// breathe; below this the bordered look just collides with the edge.
const minBorderedPromptWidth = 50

func promptInputContentWidth(termWidth int) int {
	paletteWidth, _ := promptPaletteWidth(termWidth)
	return max(paletteWidth-4, 1)
}

func questionDefaultIndex(options []string, defaultValue string) int {
	defaultValue = normalizeTimelineText(defaultValue)
	if defaultValue == "" {
		return 0
	}
	for i, option := range options {
		if normalizeTimelineText(option) == defaultValue {
			return i
		}
	}
	return 0
}

func questionOptionVisibleCount(height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return min(total, 8)
	}
	return min(total, max(height-10, 1))
}

func promptWrappedText(text string, width, maxLines int) string {
	text = normalizeTimelineText(text)
	if text == "" || width <= 0 || maxLines <= 0 {
		return ""
	}
	wrapped := wrapText(text, width)
	lines := strings.Split(wrapped, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = truncateForWidth(strings.TrimSpace(lines[maxLines-1])+" …", width)
	}
	return strings.Join(lines, "\n")
}

func permissionSelectedIndex(index int) int {
	if index < 0 || index > int(PermissionDeny) {
		return int(PermissionDeny)
	}
	return index
}

// wrapPromptContainer wraps the rendered content in a rounded-border
// container if `bordered` is true, otherwise returns content as-is. Used
// by every prompt renderer to share the overflow-guard logic.
func wrapPromptContainer(content string, width int, bordered bool, borderColor lipgloss.Color) string {
	if !bordered {
		return content
	}
	containerStyle := lipgloss.NewStyle().
		Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)
	return containerStyle.Render(content)
}

// renderPermissionPrompt renders the permission prompt UI (clean, compact style).
func (m Model) renderPermissionPrompt() string {
	if m.permRequest == nil {
		return ""
	}

	paletteWidth, bordered := promptPaletteWidth(m.width)
	compact := m.height > 0 && m.height < 18
	riskLevel := strings.ToLower(normalizeTimelineText(m.permRequest.RiskLevel))

	// Border color based on risk level
	borderColor := ColorWarning
	if riskLevel == "high" {
		borderColor = ColorError
	} else if riskLevel == "low" {
		borderColor = ColorSuccess
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(borderColor)

	labelStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)

	// Risk indicator: bold colored text, no background fill. The previous
	// pass used Background(...) + Foreground(ColorBg) to render a pill
	// shape, which looked like a UI-kit chip stamped onto the prompt and
	// fought with the title color. Bold + the existing risk color (border
	// already echoes it) gives the same urgency without the rectangle.
	var riskBadge string
	switch riskLevel {
	case "high":
		riskBadge = lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render("HIGH RISK")
	case "medium":
		riskBadge = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("MEDIUM RISK")
	case "low":
		riskBadge = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true).Render("LOW RISK")
	default:
		riskBadge = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("RISK UNKNOWN")
	}

	var builder strings.Builder

	// Title line with badge. Stack them when the terminal is too narrow; forcing
	// both labels onto one row made the most safety-critical prompt overflow.
	headerWidth := paletteWidth
	if !bordered {
		headerWidth = max(m.width, 1)
	}
	title := "Permission Required"
	if headerWidth < lipgloss.Width(title) {
		title = "Permission"
	}
	title = truncateForWidth(title, headerWidth)
	riskBadge = fitPanelContent(riskBadge, headerWidth)
	headerInline := lipgloss.Width(title)+2+lipgloss.Width(riskBadge) <= headerWidth
	builder.WriteString(titleStyle.Render(title))
	if headerInline {
		builder.WriteString("  ")
	} else {
		builder.WriteString("\n")
	}
	builder.WriteString(riskBadge)
	if compact {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n")
	}

	// Tool info with operation context
	toolName := normalizeTimelineText(m.permRequest.ToolName)
	if toolName == "" {
		toolName = "unknown tool"
	}
	opLabel := normalizeTimelineText(formatPermissionOperation(toolName, m.permRequest.Args))
	opLabel = truncateForWidth(opLabel, max(paletteWidth-lipgloss.Width("  ▸ Tool: "), 1))
	toolLine := markerStyle.Render("  ▸ ") + labelStyle.Render("Tool: ") + valueStyle.Render(opLabel)
	builder.WriteString(fitPanelContent(toolLine, paletteWidth))
	builder.WriteString("\n")

	if m.permShowDetails {
		if !compact {
			builder.WriteString("\n")
		}
		detailTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorInfo)
		builder.WriteString(fitPanelContent(detailTitleStyle.Render("  Request details"), paletteWidth))
		builder.WriteString("\n")

		detailWidth := max(1, paletteWidth-6)
		lines := permissionDetailLines(m.permRequest, detailWidth)
		visible := permissionDetailVisibleCount(m.height, len(lines))
		maxScroll := max(0, len(lines)-visible)
		start := min(max(m.permDetailScroll, 0), maxScroll)
		end := min(start+visible, len(lines))
		moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		if start > 0 {
			marker := truncateForWidth(fmt.Sprintf("↑ %d more lines", start), max(paletteWidth-2, 1))
			fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
		}
		for _, line := range lines[start:end] {
			builder.WriteString(valueStyle.Render(line))
			builder.WriteString("\n")
		}
		if end < len(lines) {
			marker := truncateForWidth(fmt.Sprintf("↓ %d more lines", len(lines)-end), max(paletteWidth-2, 1))
			fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
		}

		if !compact {
			builder.WriteString("\n")
		}
		if notice := safeKeyEntryText(m.permNotice); notice != "" {
			warning := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render(notice)
			builder.WriteString(fitPanelContent(warning, paletteWidth))
			builder.WriteString("\n")
		}
		footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(max(paletteWidth-4, 1))
		footer := "  Esc Deny  ·  ? Back  ·  1-3 Decide  ·  ↑/↓ Scroll"
		if isUnavailablePromptNotice(m.permNotice) {
			footer = "  Esc Cancel  ·  Response unavailable"
		} else if compact {
			footer = "  Esc Deny · ? Back · 1-3 Decide · ↑/↓ Scroll"
		}
		builder.WriteString(renderPromptFooterLine(footerStyle, max(paletteWidth-4, 1), footer))
		builder.WriteString("\n")
		return wrapPromptContainer(builder.String(), paletteWidth, bordered, borderColor)
	}

	// Command/Details — show the key argument value
	if len(m.permRequest.Args) > 0 {
		detail := ""
		detailBudget := max(paletteWidth-10, 1)
		for _, key := range []string{"command", "file_path", "path", "pattern", "url"} {
			if val, ok := m.permRequest.Args[key].(string); ok && normalizeTimelineText(val) != "" {
				detail = truncateForWidth(normalizeTimelineText(val), detailBudget)
				break
			}
		}
		if detail != "" {
			builder.WriteString(fitPanelContent(markerStyle.Render("    ")+valueStyle.Render(detail), paletteWidth))
			builder.WriteString("\n")
		}
	}

	// Reason (compact)
	if reasonText := normalizeTimelineText(m.permRequest.Reason); reasonText != "" {
		reasonBudget := max(paletteWidth-10, 1)
		reason := truncateForWidth(reasonText, reasonBudget)
		builder.WriteString(fitPanelContent(markerStyle.Render("    ")+labelStyle.Render(reason), paletteWidth))
		builder.WriteString("\n")
	}

	if !compact {
		builder.WriteString("\n")
	}

	// Vertical numbered options — matches the question prompt + model selector
	// (`1. … 2. …` with a `> ` selected marker) instead of a dense y/a/n letter
	// row. ↑/↓ + Enter, or press the number; the y/a/n quick keys still work.
	// Local styles (mirroring ModalNormal/ModalSelected) keep this renderer free
	// of the m.styles pointer so bare-Model tests don't nil-panic.
	normalOpt := lipgloss.NewStyle().Foreground(ColorMuted)
	selectedOpt := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	selected := permissionSelectedIndex(m.permSelectedOption)
	for i, opt := range []string{"Allow once", "Allow for session", "Deny"} {
		prefix := "  "
		style := normalOpt
		if i == selected {
			prefix = "> "
			style = selectedOpt
		}
		label := truncateForWidth(fmt.Sprintf("%d. %s", i+1, opt), max(paletteWidth-2, 1))
		fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render(label))
	}

	if !compact {
		builder.WriteString("\n")
	}
	if notice := safeKeyEntryText(m.permNotice); notice != "" {
		warning := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render(notice)
		builder.WriteString(fitPanelContent(warning, paletteWidth))
		builder.WriteString("\n")
	}
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(max(paletteWidth-4, 1))
	if isUnavailablePromptNotice(m.permNotice) {
		builder.WriteString(renderPromptFooterLine(footerStyle, max(paletteWidth-4, 1), "  Esc Cancel  ·  Response unavailable"))
	} else if compact {
		builder.WriteString(renderPromptFooterLine(footerStyle, max(paletteWidth-4, 1), "  Esc Deny · ? Details · 1-3 · Enter · ↑/↓"))
	} else {
		builder.WriteString(footerStyle.Render("  ↑/↓ Navigate  ·  1-3 Select  ·  Enter Confirm"))
		builder.WriteString("\n")
		builder.WriteString(renderPromptFooterLine(footerStyle, max(paletteWidth-4, 1), "  Esc Deny  ·  ? Details"))
	}
	builder.WriteString("\n")

	if !bordered {
		return builder.String()
	}
	containerStyle := lipgloss.NewStyle().
		Width(paletteWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1)
	return containerStyle.Render(builder.String())
}

func permissionDetailLines(req *PermissionRequestMsg, width int) []string {
	if req == nil {
		return nil
	}
	width = max(width, 8)
	lines := make([]string, 0, len(req.Args)+3)
	if strings.TrimSpace(req.Reason) != "" {
		lines = append(lines, wrapPermissionField("Reason", req.Reason, width)...)
	}
	if len(req.Args) == 0 {
		lines = append(lines, "  Arguments: none")
		return lines
	}

	keys := make([]string, 0, len(req.Args))
	for key := range req.Args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, wrapPermissionField(key, permissionValueText(req.Args[key]), width)...)
	}
	return lines
}

func permissionValueText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if data, err := json.Marshal(value); err == nil {
		return string(data)
	}
	return fmt.Sprint(value)
}

func wrapPermissionField(label, value string, width int) []string {
	width = max(width, 1)
	label = normalizeTimelineText(label)
	if label == "" {
		label = "value"
	}
	prefix := "  " + label + ": "
	continuationWidth := lipgloss.Width(prefix)
	var lines []string
	if continuationWidth >= width {
		lines = append(lines, truncateForWidth(prefix, width))
		continuationWidth = min(2, max(width-1, 0))
		prefix = strings.Repeat(" ", continuationWidth)
	}
	continuation := strings.Repeat(" ", continuationWidth)
	if value == "" {
		value = "(empty)"
	}

	for sourceLine := range strings.SplitSeq(value, "\n") {
		linePrefix := prefix
		remaining := []rune(sourceLine)
		if len(remaining) == 0 {
			lines = append(lines, linePrefix)
			prefix = continuation
			continue
		}
		for len(remaining) > 0 {
			available := max(1, width-lipgloss.Width(linePrefix))
			take := permissionCellPrefix(remaining, available)
			chunk := string(remaining[:take])
			lines = append(lines, linePrefix+truncateForWidth(chunk, available))
			remaining = remaining[take:]
			linePrefix = continuation
		}
		prefix = continuation
	}
	return lines
}

func permissionCellPrefix(runes []rune, width int) int {
	used := 0
	for i, r := range runes {
		cellWidth := lipgloss.Width(string(r))
		if used+cellWidth > width {
			if i == 0 {
				return 1
			}
			return i
		}
		used += cellWidth
	}
	return len(runes)
}

func permissionDetailVisibleCount(height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return total
	}
	return min(total, max(1, height-12))
}

// formatPermissionOperation returns a descriptive label like "bash → Run command"
// instead of just the tool name.
func formatPermissionOperation(toolName string, args map[string]any) string {
	switch toolName {
	case "bash":
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			// Extract first word of command as verb
			parts := strings.Fields(cmd)
			if len(parts) > 0 {
				return fmt.Sprintf("bash → Run: %s", parts[0])
			}
		}
		return "bash → Run command"
	case "write":
		if _, ok := args["file_path"].(string); ok {
			return "write → Create/overwrite file"
		}
		return "write → Create file"
	case "edit":
		return "edit → Modify file"
	case "delete":
		return "delete → Remove file"
	case "ssh":
		return "ssh → Remote command"
	case "git_commit":
		return "git_commit → Create commit"
	case "git_add":
		return "git_add → Stage files"
	case "copy":
		return "copy → Copy file"
	case "move":
		return "move → Move/rename file"
	case "mkdir":
		return "mkdir → Create directory"
	case "task":
		if t, ok := args["type"].(string); ok && t != "" {
			return fmt.Sprintf("task → Spawn %s agent", t)
		}
		return "task → Spawn sub-agent"
	case "mcp_admin":
		action, _ := args["action"].(string)
		if action == "" {
			action = "list"
		}
		return fmt.Sprintf("mcp_admin → %s", action)
	default:
		return toolName
	}
}

// renderQuestionPrompt renders the question prompt UI.
func (m Model) renderQuestionPrompt() string {
	if m.questionRequest == nil {
		return ""
	}

	var builder strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(paletteWidth-4, 1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary)

	// Header
	builder.WriteString(titleStyle.Render(truncateForWidth("Question from Agent", contentWidth)))
	builder.WriteString("\n\n")

	// Question text (wrap nicely)
	questionStyle := lipgloss.NewStyle().Foreground(ColorText)
	question := promptWrappedText(m.questionRequest.Question, contentWidth, 3)
	if question == "" {
		question = "The agent is waiting for your response."
	}
	builder.WriteString(questionStyle.Render(question))
	builder.WriteString("\n\n")

	// If custom input mode or no options, show input
	if m.questionCustomInput || len(m.questionRequest.Options) == 0 {
		builder.WriteString("  " + m.questionInputModel.View())
		builder.WriteString("\n")
		if m.questionInputError != "" {
			validationStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
			builder.WriteString(validationStyle.Render("  ⚠ " + m.questionInputError))
			builder.WriteString("\n")
		}
		builder.WriteString("\n")

		footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(contentWidth)

		unavailable := isUnavailablePromptNotice(m.questionInputError)
		if m.questionCustomInput && unavailable {
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc Back  ·  Submission unavailable"))
		} else if unavailable {
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc Cancel  ·  Submission unavailable"))
		} else if m.questionCustomInput {
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc Back  ·  Enter Submit  ·  Alt+Enter New line"))
		} else {
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc Cancel  ·  Enter Submit  ·  Alt+Enter New line"))
		}
		builder.WriteString("\n")
		return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorSecondary)
	}

	optionCount := len(m.questionRequest.Options) + 1 // + custom answer
	selected := min(max(m.questionSelectedOption, 0), optionCount-1)
	visible := questionOptionVisibleCount(m.height, optionCount)
	start := selected - visible/2
	start = min(max(start, 0), max(optionCount-visible, 0))
	end := min(start+visible, optionCount)
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	if start > 0 {
		marker := truncateForWidth(fmt.Sprintf("↑ %d more answer(s)", start), max(contentWidth-2, 1))
		fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
	}

	for i := start; i < end; i++ {
		prefix := "  "
		style := m.styles.ModalNormal
		if i == selected {
			prefix = "> "
			style = m.styles.ModalSelected
		}
		label := "Other (custom answer)"
		if i < len(m.questionRequest.Options) {
			option := normalizeTimelineText(m.questionRequest.Options[i])
			if option == "" {
				option = fmt.Sprintf("Option %d", i+1)
			}
			label = fmt.Sprintf("%d. %s", i+1, option)
			if normalizeTimelineText(m.questionRequest.Options[i]) == normalizeTimelineText(m.questionRequest.Default) && normalizeTimelineText(m.questionRequest.Default) != "" {
				label += " (default)"
			}
		}
		fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render(truncateForWidth(label, max(contentWidth-2, 1))))
	}
	if end < optionCount {
		marker := truncateForWidth(fmt.Sprintf("↓ %d more answer(s)", optionCount-end), max(contentWidth-2, 1))
		fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
	}

	builder.WriteString("\n")
	if m.questionInputError != "" {
		validationStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		builder.WriteString(fitPanelContent(validationStyle.Render("  ⚠ "+m.questionInputError), contentWidth))
		builder.WriteString("\n")
	}

	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(contentWidth)
	quickMax := min(9, len(m.questionRequest.Options))
	quickLabel := "1 Select"
	if quickMax > 1 {
		quickLabel = fmt.Sprintf("1-%d Select", quickMax)
	}
	navigation := ""
	if optionCount > 1 {
		navigation = "↑/↓ Navigate"
	}
	if navigation != "" && visible < optionCount {
		navigation += " · PgUp/PgDn Page"
	}
	primaryFooter := fmt.Sprintf("  Esc Cancel  ·  Enter Confirm  ·  %s", quickLabel)
	if isUnavailablePromptNotice(m.questionInputError) {
		primaryFooter = "  Esc Cancel  ·  Response unavailable"
		navigation = ""
	}
	if navigation != "" && visible >= optionCount && lipgloss.Width(primaryFooter)+lipgloss.Width("  ·  "+navigation) <= contentWidth {
		primaryFooter += "  ·  " + navigation
	}
	builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, primaryFooter))
	if navigation != "" && visible < optionCount {
		// Paging is a real available action, not decorative metadata. Give it a
		// controlled second row instead of letting implicit wrapping decide where
		// recovery and navigation land; the frame compositor preserves footer rows.
		builder.WriteString("\n")
		builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  "+navigation))
	}
	builder.WriteString("\n")

	return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorSecondary)
}

// renderPlanApproval renders the plan approval UI.
func (m Model) renderPlanApproval() string {
	if m.planRequest == nil {
		return ""
	}

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(paletteWidth-4, 1)
	compact := m.height > 0 && m.height < 18

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	planTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	descStyle := lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)
	stepNumStyle := lipgloss.NewStyle().Foreground(ColorDim)
	stepTitleStyle := lipgloss.NewStyle().Foreground(ColorText)
	stepDescStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	labelStyle := lipgloss.NewStyle().Foreground(ColorInfo)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(contentWidth)

	var b strings.Builder

	// Header: title + step count (the rounded container is added once at the end
	// via wrapPromptContainer — no hand-drawn box / manual padding any more).
	header := titleStyle.Render("Plan Approval") + "  " + subtitleStyle.Render(fmt.Sprintf("%d steps", len(m.planRequest.Steps)))
	b.WriteString(fitPanelContent(header, contentWidth))
	if compact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}

	// Plan title + description.
	planTitle := normalizeTimelineText(m.planRequest.Title)
	if planTitle == "" {
		planTitle = "Untitled plan"
	}
	b.WriteString("  " + planTitleStyle.Render(truncateForWidth(planTitle, max(contentWidth-2, 1))) + "\n")
	if d := promptWrappedText(m.planRequest.Description, max(contentWidth-2, 1), map[bool]int{true: 1, false: 2}[compact]); d != "" {
		b.WriteString("  " + descStyle.Render(d) + "\n")
	}
	if !compact {
		b.WriteString("\n")
	}

	// Steps.
	if !m.planFeedbackMode {
		if len(m.planRequest.Steps) == 0 {
			empty := labelStyle.Render("Steps:") + " " + subtitleStyle.Render("No executable steps supplied · request changes or reject")
			b.WriteString("  " + fitPanelContent(empty, max(contentWidth-2, 1)) + "\n")
		} else {
			visible := planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps))
			start := min(max(m.planStepScroll, 0), max(len(m.planRequest.Steps)-visible, 0))
			end := min(start+visible, len(m.planRequest.Steps))
			b.WriteString("  " + labelStyle.Render("Steps:") + "\n")
			if start > 0 {
				marker := truncateForWidth(fmt.Sprintf("↑ %d earlier step(s)", start), max(contentWidth-2, 1))
				b.WriteString("  " + subtitleStyle.Render(marker) + "\n")
			}
			for i := start; i < end; i++ {
				step := m.planRequest.Steps[i]
				stepID := step.ID
				if stepID <= 0 {
					stepID = i + 1
				}
				stepTitle := normalizeTimelineText(step.Title)
				if stepTitle == "" {
					stepTitle = "Untitled step"
				}
				line := fmt.Sprintf("Step %d: %s", stepID, stepTitle)
				b.WriteString("  " + stepNumStyle.Render("○") + " " + stepTitleStyle.Render(truncateForWidth(line, max(contentWidth-4, 1))) + "\n")
				if !compact {
					if sd := normalizeTimelineText(step.Description); sd != "" {
						b.WriteString("     " + stepDescStyle.Render(truncateForWidth(sd, max(contentWidth-5, 1))) + "\n")
					}
				}
			}
			if end < len(m.planRequest.Steps) {
				marker := truncateForWidth(fmt.Sprintf("↓ %d more step(s)", len(m.planRequest.Steps)-end), max(contentWidth-2, 1))
				b.WriteString("  " + subtitleStyle.Render(marker) + "\n")
			}
		}
	}

	if contractName := normalizeTimelineText(m.planRequest.ContractName); contractName != "" {
		contract := "Contract: " + contractName
		if intent := normalizeTimelineText(m.planRequest.Intent); intent != "" && !compact {
			contract += " · " + intent
		}
		b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorAccent).Render(truncateForWidth(contract, max(contentWidth-2, 1))) + "\n")
		guards := len(m.planRequest.Boundaries) + len(m.planRequest.Invariants)
		if guards > 0 || len(m.planRequest.Examples) > 0 {
			summary := truncateForWidth(fmt.Sprintf("%d guardrail(s) · %d example(s)", guards, len(m.planRequest.Examples)), max(contentWidth-2, 1))
			b.WriteString("  " + subtitleStyle.Render(summary) + "\n")
		}
	}

	if !compact {
		b.WriteString("\n")
	}

	// Feedback sub-mode: collect modification text inside the SAME container.
	if m.planFeedbackMode {
		b.WriteString("  " + labelStyle.Render("Enter your feedback:") + "\n")
		b.WriteString("  " + m.planFeedbackInput.View() + "\n")
		if m.planFeedbackError != "" {
			validationStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
			b.WriteString(validationStyle.Render("  ⚠ " + m.planFeedbackError))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		feedbackFooter := "  Esc Back  ·  Enter Submit  ·  Alt+Enter New line"
		if isUnavailablePromptNotice(m.planFeedbackError) {
			feedbackFooter = "  Esc Back  ·  Submission unavailable"
		}
		b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, feedbackFooter))
		return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPlan)
	}

	// Numbered options — the same idiom as the question/permission prompts (no
	// Background-fill highlight, no per-option ✓/✗/✎ icons). Quick keys y/n/m
	// (and 1/2/3) still work via the handler.
	normalOpt := lipgloss.NewStyle().Foreground(ColorMuted)
	selectedOpt := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan)
	selectedOption := m.planSelectedOption
	if len(m.planRequest.Steps) == 0 && selectedOption == int(PlanApproved) {
		selectedOption = int(PlanRejected)
	}
	for i, opt := range []string{"Approve", "Reject", "Request changes"} {
		prefix := "  "
		style := normalOpt
		if len(m.planRequest.Steps) == 0 && i == int(PlanApproved) {
			opt += " (unavailable · no steps)"
			style = lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		} else if i == selectedOption {
			prefix = "> "
			style = selectedOpt
		}
		label := truncateForWidth(fmt.Sprintf("%d. %s", i+1, opt), max(contentWidth-2, 1))
		fmt.Fprintf(&b, "%s%s\n", prefix, style.Render(label))
	}

	if !compact {
		b.WriteString("\n")
	}
	if notice := safeKeyEntryText(m.planApprovalNotice); notice != "" {
		warning := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render(notice)
		b.WriteString(fitPanelContent(warning, contentWidth))
		b.WriteString("\n")
	}
	footer := "  Esc Cancel  ·  Enter Confirm  ·  1-3 Select  ·  ↑/↓ Choose"
	if isUnavailablePromptNotice(m.planApprovalNotice) {
		footer = "  Esc Cancel  ·  Response unavailable"
	} else if len(m.planRequest.Steps) == 0 {
		footer = "  Esc Cancel  ·  Enter Confirm  ·  2-3 Select  ·  ↑/↓ Choose"
	}
	if len(m.planRequest.Steps) > planApprovalStepVisibleCount(m.height, len(m.planRequest.Steps)) {
		footer += "  ·  PgUp/PgDn Steps"
	}
	b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPlan)
}

func planApprovalStepVisibleCount(height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return min(total, 6)
	}
	if height < 18 {
		return 1
	}
	return min(total, max((height-12)/2, 1))
}

// renderModelSelector renders the model selector UI.
func (m Model) renderModelSelector() string {
	var builder strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(paletteWidth-4, 1)
	compact := m.height > 0 && m.height < 18

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary)

	subtitleStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	// Header
	header := titleStyle.Render("Select Model") + "  " + subtitleStyle.Render("Ctrl+K")
	builder.WriteString(fitPanelContent(header, contentWidth))
	if compact {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n")
	}

	// Current model info. Prefer the friendly name; the raw provider ID is
	// useful internally but needlessly noisy as the primary user-facing label.
	currentLabel := safeKeyEntryText(m.currentModel)
	for _, model := range m.availableModels {
		if safeKeyEntryText(model.ID) == m.currentModel && safeKeyEntryText(model.Name) != "" {
			currentLabel = safeKeyEntryText(model.Name)
			break
		}
	}
	if currentLabel == "" {
		currentLabel = "not set"
	}
	currentLine := "Current: " + currentLabel
	fmt.Fprintf(&builder, "  %s\n", m.styles.Spinner.Render(truncateForWidth(currentLine, max(contentWidth-2, 1))))
	if !compact {
		builder.WriteString("\n")
	}

	escapeAction := "Close"
	if m.modelSelectorReturnState == StateSettings {
		escapeAction = "Back to settings"
	}

	if len(m.availableModels) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(ColorMuted).
			Italic(true).
			Width(contentWidth)
		builder.WriteString(emptyStyle.Render("  No model choices are registered for the active provider."))
		builder.WriteString("\n")
		builder.WriteString(emptyStyle.Render("  Run /provider to choose a supported provider, or /doctor to inspect setup."))
		builder.WriteString("\n")
		footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(contentWidth)
		builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc "+escapeAction))
		builder.WriteString("\n")
		return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorPrimary)
	}

	selectedIndex := m.modelSelectedIndex
	if selectedIndex < 0 || selectedIndex >= len(m.availableModels) {
		selectedIndex = 0
	}
	visibleCount := modelSelectorVisibleCount(m.height, len(m.availableModels))
	start := selectedIndex - visibleCount/2
	if start < 0 {
		start = 0
	}
	if start > len(m.availableModels)-visibleCount {
		start = len(m.availableModels) - visibleCount
	}
	end := min(start+visibleCount, len(m.availableModels))

	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	if start > 0 {
		marker := truncateForWidth(fmt.Sprintf("↑ %d more", start), max(contentWidth-2, 1))
		fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
	}

	// Model options — height-aware so the selected row remains visible instead
	// of relying on the outer frame to clip an arbitrarily long modal.
	for i := start; i < end; i++ {
		model := m.availableModels[i]
		prefix := "  "
		style := m.styles.ModalNormal
		if i == selectedIndex {
			prefix = "> "
			style = m.styles.ModalSelected
		}

		// Show number for quick select
		modelID := safeKeyEntryText(model.ID)
		name := safeKeyEntryText(model.Name)
		if name == "" {
			name = modelID
		}
		if name == "" {
			name = "Unnamed model"
		}
		label := fmt.Sprintf("%d. %s", i+1, name)
		if modelID == "" {
			label += " (unavailable)"
		} else if modelID == m.currentModel {
			label += " (current)"
		}
		fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render(truncateForWidth(label, max(contentWidth-2, 1))))

		// Muted description
		description := safeKeyEntryText(model.Description)
		if description == "" {
			description = "No description provided."
		}
		fmt.Fprintf(&builder, "     %s\n", m.styles.ModalMuted.Render(truncateForWidth(description, max(contentWidth-5, 1))))
	}
	if end < len(m.availableModels) {
		marker := truncateForWidth(fmt.Sprintf("↓ %d more", len(m.availableModels)-end), max(contentWidth-2, 1))
		fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
	}

	if notice := safeKeyEntryText(m.modelSelectorNotice); notice != "" {
		noticeColor := ColorWarning
		lowerNotice := strings.ToLower(notice)
		if strings.Contains(lowerNotice, "switched to") {
			noticeColor = ColorSuccess
		} else if strings.Contains(lowerNotice, "switching") || strings.Contains(lowerNotice, "wait") || strings.Contains(lowerNotice, "applying") {
			noticeColor = ColorInfo
		}
		noticeLine := lipgloss.NewStyle().Foreground(noticeColor).Bold(true).Render(notice)
		builder.WriteString(fitPanelContent(noticeLine, contentWidth))
		builder.WriteString("\n")
	}
	if !compact {
		builder.WriteString("\n")
	}

	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Width(contentWidth)
	quickMax := min(9, len(m.availableModels))
	quickLabel := "1 Select"
	if quickMax > 1 {
		quickLabel = fmt.Sprintf("1-%d Select", quickMax)
	}
	footer := fmt.Sprintf("  Esc %s · Enter Confirm · %s", escapeAction, quickLabel)
	if len(m.availableModels) > 1 {
		footer += " · ↑/↓ Navigate"
	}
	if m.onModelSelect == nil {
		footer = "  Esc " + escapeAction + " · Read-only"
	} else if m.modelSwitchPending != "" {
		footer = "  Esc " + escapeAction + " · Applying…"
		if len(m.availableModels) > 1 {
			footer += " · ↑/↓ Navigate"
		}
	}
	builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))
	builder.WriteString("\n")

	return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorPrimary)
}

func modelSelectorVisibleCount(height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return total
	}
	// Header/current/footer/container consume roughly eleven rows. Each model
	// uses a label and description row; reserve room for scroll markers too.
	return min(total, max(1, (height-11)/2))
}

// renderShortcutsOverlay renders the keyboard shortcuts overlay.
func (m Model) renderShortcutsOverlay() string {
	if m.shortcutsOverlay != nil && m.shortcutsOverlay.IsVisible() {
		return m.shortcutsOverlay.View(m.width, m.height)
	}

	// Degraded construction paths still use the same searchable, adaptive
	// source of truth. Keeping a second hard-coded table here previously let
	// removed bindings linger indefinitely.
	fallback := NewShortcutsOverlay(m.styles)
	fallback.Show()
	return fallback.View(m.width, m.height)
}
