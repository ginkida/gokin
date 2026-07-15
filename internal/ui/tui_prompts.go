package ui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/rivo/uniseg"
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

// promptPrimaryActionReadableWidth is the smallest positive terminal width at
// which the compact prompt layouts can show more than a bare option number or
// checkbox marker. Zero remains the pre-WindowSizeMsg "unconstrained" sentinel.
const promptPrimaryActionReadableWidth = 20

func promptPrimaryActionGeometryReadable(width, height, minHeight int) bool {
	widthReadable := width <= 0 || width >= promptPrimaryActionReadableWidth
	heightReadable := height <= 0 || height >= minHeight
	return widthReadable && heightReadable
}

// resizeRecoveryLabel keeps both the resize requirement and a safe escape path
// whenever the available row is wide enough. At truly degenerate widths the
// one-cell arrow is still an honest, visible recovery signal.
func resizeRecoveryLabel(width int, recovery string) string {
	width = max(width, 1)
	for _, candidate := range []string{
		"Resize · " + recovery,
		"↔ " + recovery,
		"Resize",
		"↔",
	} {
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return "↔"
}

// primaryActionFooterLabel preserves the safe recovery key and the primary
// action key when the descriptive footer does not fit. Readability gates use
// this compact form: an action is enabled only when its target and its key are
// both present in the final frame.
func primaryActionFooterLabel(width int, full, recovery, primary string) string {
	width = max(width, 1)
	recoveryKey := strings.Fields(recovery)
	compactRecovery := recovery
	if len(recoveryKey) > 0 {
		compactRecovery = recoveryKey[0]
	}
	for _, candidate := range []string{
		full,
		recovery + " · " + primary,
		recovery + " " + primary,
		compactRecovery + " · " + primary,
		primary,
	} {
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return truncateForWidth(primary, width)
}

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

type viewportWindow struct {
	start, end           int
	showAbove, showBelow bool
}

func chooseViewportWindow(total, selected, rowBudget int) viewportWindow {
	return chooseFixedRowViewportWindow(total, selected, rowBudget, 1)
}

func chooseFixedRowViewportWindow(total, selected, rowBudget, itemRows int) viewportWindow {
	if total <= 0 {
		return viewportWindow{}
	}
	selected = min(max(selected, 0), total-1)
	rowBudget = max(rowBudget, 1)
	itemRows = max(itemRows, 1)

	bestStart, bestEnd := selected, selected+1
	bestRows, bestDistance := 0, int(^uint(0)>>1)
	for start := 0; start <= selected; start++ {
		for end := selected + 1; end <= total; end++ {
			cost := (end - start) * itemRows
			if start > 0 {
				cost++
			}
			if end < total {
				cost++
			}
			if cost > rowBudget {
				continue
			}
			dataRows := end - start
			distance := (start + end - 1) - 2*selected
			if distance < 0 {
				distance = -distance
			}
			if dataRows > bestRows || (dataRows == bestRows && distance < bestDistance) {
				bestStart, bestEnd = start, end
				bestRows, bestDistance = dataRows, distance
			}
		}
	}

	window := viewportWindow{start: bestStart, end: bestEnd}
	used := (bestEnd - bestStart) * itemRows
	if bestRows > 0 {
		window.showAbove = bestStart > 0
		window.showBelow = bestEnd < total
		return window
	}
	window.showAbove = bestStart > 0 && used < rowBudget
	if window.showAbove {
		used++
	}
	window.showBelow = bestEnd < total && used < rowBudget
	return window
}

type modelSelectorLayout struct {
	compact, showDescriptions bool
	window                    viewportWindow
}

func (m Model) buildModelSelectorLayout(bordered bool) modelSelectorLayout {
	layout := modelSelectorLayout{
		compact:          m.height > 0 && m.height < 18,
		showDescriptions: m.height <= 0 || m.height >= 12,
	}
	total := len(m.availableModels)
	if total == 0 {
		return layout
	}
	if m.height <= 0 {
		layout.window = viewportWindow{start: 0, end: total}
		return layout
	}

	fixedRows := 0
	if layout.compact {
		fixedRows += 2 // header + current model
	} else {
		fixedRows += 5 // spaced header/current rows + pre-footer breathing row
	}
	if safeKeyEntryText(m.modelSelectorNotice) != "" {
		fixedRows++
	}
	fixedRows += 2 // footer + trailing breathing row
	if bordered {
		fixedRows += 2
	}
	itemRows := 1
	if layout.showDescriptions {
		itemRows = 2
	}
	layout.window = chooseFixedRowViewportWindow(total, m.modelSelectedIndex, max(m.height-fixedRows, 1), itemRows)
	return layout
}

func (m Model) modelSelectorPageSize() int {
	_, bordered := promptPaletteWidth(m.width)
	window := m.buildModelSelectorLayout(bordered).window
	return max(window.end-window.start, 1)
}

func (m Model) modelSelectorPrimaryActionReadable() bool {
	minHeight := 3 // selected model + local footer + global status
	if safeKeyEntryText(m.modelSelectorNotice) != "" {
		minHeight++
	}
	_, bordered := promptPaletteWidth(m.width)
	if bordered {
		// The selector intentionally ends its content with a newline; inside a
		// bordered card that leaves a breathing row plus the bottom border.
		minHeight += 2
	}
	return promptPrimaryActionGeometryReadable(m.width, m.height, minHeight)
}

func questionPromptMaxLines(height int) int {
	switch {
	case height > 0 && height < 14:
		return 1
	case height > 0 && height < 18:
		return 2
	default:
		return 3
	}
}

func (m Model) questionOptionWindow(total int, bordered bool) viewportWindow {
	if total <= 0 {
		return viewportWindow{}
	}
	if m.height <= 0 {
		return chooseViewportWindow(total, m.questionSelectedOption, 10)
	}
	compact := m.height < 18
	question := promptWrappedText(m.questionRequest.Question, max(promptInputContentWidth(m.width), 1), questionPromptMaxLines(m.height))
	if question == "" {
		question = "The agent is waiting for your response."
	}
	questionRows := max(lipgloss.Height(question), 1)

	fixedRows := questionRows + 1 // question + primary footer
	if compact {
		fixedRows++ // title
	} else {
		fixedRows += 4 // title+blank, question blank, list blank
		fixedRows++    // trailing breathing row
	}
	if m.questionInputError != "" {
		fixedRows++
	}
	if bordered {
		fixedRows += 2
	}

	withoutPaging := max(m.height-fixedRows, 1)
	if total <= withoutPaging {
		return viewportWindow{start: 0, end: total}
	}
	// Overflow adds a dedicated navigation footer row. Markers themselves are
	// counted by chooseViewportWindow inside the remaining list budget.
	return chooseViewportWindow(total, m.questionSelectedOption, max(withoutPaging-1, 1))
}

func (m Model) questionPageItemCount() int {
	if m.questionRequest == nil {
		return 0
	}
	_, bordered := promptPaletteWidth(m.width)
	window := m.questionOptionWindow(len(m.questionRequest.Options)+1, bordered)
	return max(window.end-window.start, 1)
}

func (m Model) questionPrimaryActionReadable() bool {
	if m.questionRequest == nil {
		return true
	}
	_, bordered := promptPaletteWidth(m.width)
	if m.questionCustomInput || len(m.questionRequest.Options) == 0 {
		// The question itself is part of the target: submitting a visible editor
		// while its question is cropped is still a hidden-action failure.
		minHeight := 5 // question + input + breathing row + footer + global status
		if safeKeyEntryText(m.questionInputError) != "" {
			minHeight++
		}
		if bordered {
			// These editor branches end with a newline inside the card.
			minHeight += 2
		}
		return promptPrimaryActionGeometryReadable(m.width, m.height, minHeight)
	}

	minHeight := 4 // question + selected answer + footer + global status
	if safeKeyEntryText(m.questionInputError) != "" {
		minHeight++
	}
	if bordered {
		minHeight++ // bottom border
	}
	return promptPrimaryActionGeometryReadable(m.width, m.height, minHeight)
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

const (
	permissionReadableDecisionWidth  = 8
	permissionReadableDecisionHeight = 3
)

// permissionAllowChoicesReadable reports whether the permission surface has
// enough geometry to show an allow choice with meaningful context. Below this
// threshold the key handler fails closed: explicit and implicit allow keys are
// ignored, while deny remains available as the safe recovery action.
func (m Model) permissionAllowChoicesReadable() bool {
	// Zero is the model's pre-WindowSizeMsg "unspecified" sentinel and existing
	// prompt renderers treat it as unconstrained. Positive dimensions are the
	// only authoritative terminal budgets.
	widthReadable := m.width <= 0 || m.width >= permissionReadableDecisionWidth
	heightReadable := m.height <= 0 || m.height >= permissionReadableDecisionHeight
	return widthReadable && heightReadable
}

// permissionKeyWouldAllow distinguishes unsafe confirmation from fail-closed
// denial. Enter/Space inherit the highlighted choice; direct allow shortcuts
// always count as allow even when the highlighted row is Deny.
func permissionKeyWouldAllow(key string, selected int) bool {
	switch key {
	case "enter", " ":
		return permissionSelectedIndex(selected) != int(PermissionDeny)
	case "1", "2", "y", "a":
		return true
	default:
		return false
	}
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
	// At six rows or fewer the normal card cannot coexist with the persistent
	// status bar. Render the three pieces required to make a safe decision. The
	// selected action comes last because the frame compositor keeps bottom rows;
	// at height three it survives while the tiny status bar carries recovery.
	// At smaller heights the prompt cannot survive, so allow keys fail closed.
	if m.height > 0 && m.height <= 6 {
		return m.renderTinyPermissionPrompt()
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
		visible := m.permissionDetailPageSize(len(lines))
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
		footer := "  Esc Deny  ·  ? Back  ·  1-3 Decide"
		if isUnavailablePromptNotice(m.permNotice) {
			footer = "  Esc Cancel  ·  ? Back  ·  Response unavailable"
		}
		if maxScroll > 0 {
			footer += "  ·  ↑/↓ Scroll"
		}
		if compact {
			footer = strings.ReplaceAll(footer, "  ·  ", " · ")
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

func (m Model) renderTinyPermissionPrompt() string {
	width := max(m.width, 1)
	riskLevel := strings.ToLower(normalizeTimelineText(m.permRequest.RiskLevel))
	risk := "RISK UNKNOWN"
	riskColor := ColorWarning
	switch riskLevel {
	case "high":
		risk = "HIGH RISK"
		riskColor = ColorError
	case "medium":
		risk = "MEDIUM RISK"
	case "low":
		risk = "LOW RISK"
		riskColor = ColorSuccess
	}

	selected := permissionSelectedIndex(m.permSelectedOption)
	options := []string{"Allow once", "Allow for session", "Deny"}
	decision := fmt.Sprintf("> %d. %s", selected+1, options[selected])
	if width < 18 {
		compactOptions := []string{"ONCE", "SESS", "DENY"}
		decision = fmt.Sprintf("> %d. %s", selected+1, compactOptions[selected])
		if width < 10 {
			decision = fmt.Sprintf("%d %s", selected+1, compactOptions[selected])
		}
	}
	footer := "Esc Deny · 1-3"
	unavailable := isUnavailablePromptNotice(m.permNotice)
	if unavailable {
		footer = "Esc Cancel"
	} else if m.permShowDetails {
		footer = "Esc Deny · ? Back"
	}
	tool := normalizeTimelineText(m.permRequest.ToolName)
	if tool == "" {
		tool = "unknown tool"
	}
	context := risk + " · " + tool
	if width < 18 {
		riskCode := "?"
		switch riskLevel {
		case "high":
			riskCode = "H"
		case "medium":
			riskCode = "M"
		case "low":
			riskCode = "L"
		}
		context = riskCode + " " + tool
	}
	if unavailable {
		decision = "Response unavailable"
	}

	riskStyle := lipgloss.NewStyle().Bold(true).Foreground(riskColor)
	decisionStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	contextRow := riskStyle.Render(truncateForWidth(context, width))
	decisionRow := decisionStyle.Render(truncateForWidth(decision, width))
	footerRow := footerStyle.Render(truncateForWidth(footer, width))
	if m.height <= 3 {
		// Footer comes first so bottom-tail cropping keeps context plus the
		// selected decision; the persistent status row carries Esc denial.
		return strings.Join([]string{footerRow, contextRow, decisionRow}, "\n")
	}
	return strings.Join([]string{contextRow, decisionRow, footerRow}, "\n")
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
	// Permission arguments are untrusted pre-approval data. Make terminal
	// controls visible instead of executing or silently stripping them; tabs
	// are rendered as a control picture so they cannot move the terminal cursor.
	value = strings.ReplaceAll(visibleTerminalControlText(value), "\t", "␉")

	for sourceLine := range strings.SplitSeq(value, "\n") {
		linePrefix := prefix
		clusters := permissionGraphemeClusters(sourceLine)
		if len(clusters) == 0 {
			lines = append(lines, linePrefix)
			prefix = continuation
			continue
		}
		for len(clusters) > 0 {
			available := max(1, width-lipgloss.Width(linePrefix))
			take := permissionClusterPrefix(clusters, available)
			chunk := strings.Join(clusters[:take], "")
			lines = append(lines, linePrefix+truncateForWidth(chunk, available))
			clusters = clusters[take:]
			linePrefix = continuation
		}
		prefix = continuation
	}
	return lines
}

func permissionGraphemeClusters(text string) []string {
	graphemes := uniseg.NewGraphemes(text)
	var clusters []string
	for graphemes.Next() {
		clusters = append(clusters, graphemes.Str())
	}
	return clusters
}

func permissionClusterPrefix(clusters []string, width int) int {
	used := 0
	for i, cluster := range clusters {
		cellWidth := lipgloss.Width(cluster)
		if used+cellWidth > width {
			if i == 0 {
				return 1
			}
			return i
		}
		used += cellWidth
	}
	return len(clusters)
}

func permissionDetailVisibleCount(height, total, noticeRows int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return total
	}
	return min(total, max(1, height-12-max(noticeRows, 0)))
}

// permissionDetailPageSize keeps the renderer and its keyboard/mouse paging on
// the same row budget. A notice occupies a real row above the footer, so it must
// reduce the detail viewport instead of pushing the safety actions off-screen.
func (m Model) permissionDetailPageSize(total int) int {
	noticeRows := 0
	if safeKeyEntryText(m.permNotice) != "" {
		noticeRows = 1
	}
	return permissionDetailVisibleCount(m.height, total, noticeRows)
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
	compact := m.height > 0 && m.height < 18

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary)

	// Header
	builder.WriteString(titleStyle.Render(truncateForWidth("Question from Agent", contentWidth)))
	if compact {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n")
	}

	// Question text (wrap nicely)
	questionStyle := lipgloss.NewStyle().Foreground(ColorText)
	question := promptWrappedText(m.questionRequest.Question, contentWidth, questionPromptMaxLines(m.height))
	if question == "" {
		question = "The agent is waiting for your response."
	}
	builder.WriteString(questionStyle.Render(question))
	if compact {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n")
	}

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
		if !m.questionPrimaryActionReadable() {
			recovery := "Esc Cancel"
			if m.questionCustomInput {
				recovery = "Esc Back"
			}
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, resizeRecoveryLabel(contentWidth, recovery)))
		} else if m.questionCustomInput && unavailable {
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc Back  ·  Submission unavailable"))
		} else if unavailable {
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  Esc Cancel  ·  Submission unavailable"))
		} else if m.questionCustomInput {
			footer := primaryActionFooterLabel(contentWidth, "Esc Back  ·  Enter Submit  ·  Alt+Enter New line", "Esc Back", "Enter")
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))
		} else {
			footer := primaryActionFooterLabel(contentWidth, "Esc Cancel  ·  Enter Submit  ·  Alt+Enter New line", "Esc Cancel", "Enter")
			builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))
		}
		builder.WriteString("\n")
		return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorSecondary)
	}

	optionCount := len(m.questionRequest.Options) + 1 // + custom answer
	selected := min(max(m.questionSelectedOption, 0), optionCount-1)
	window := m.questionOptionWindow(optionCount, bordered)
	start, end := window.start, window.end
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	if window.showAbove {
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
		inlinePosition := (start > 0 && !window.showAbove) || (end < optionCount && !window.showBelow)
		label := "Other (custom answer)"
		if strings.TrimSpace(m.questionInputModel.Value()) != "" {
			label = "Other (custom answer · draft saved)"
		}
		if i < len(m.questionRequest.Options) {
			option := normalizeTimelineText(m.questionRequest.Options[i])
			if option == "" {
				option = fmt.Sprintf("Option %d", i+1)
			}
			if inlinePosition {
				label = fmt.Sprintf("%d/%d %s", i+1, optionCount, option)
			} else {
				label = fmt.Sprintf("%d. %s", i+1, option)
			}
			if normalizeTimelineText(m.questionRequest.Options[i]) == normalizeTimelineText(m.questionRequest.Default) && normalizeTimelineText(m.questionRequest.Default) != "" {
				label += " (default)"
			}
		} else if inlinePosition {
			label = fmt.Sprintf("%d/%d %s", i+1, optionCount, label)
		}
		fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render(truncateForWidth(label, max(contentWidth-2, 1))))
	}
	if window.showBelow {
		marker := truncateForWidth(fmt.Sprintf("↓ %d more answer(s)", optionCount-end), max(contentWidth-2, 1))
		fmt.Fprintf(&builder, "  %s\n", moreStyle.Render(marker))
	}

	if !compact {
		builder.WriteString("\n")
	}
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
	// A single declared answer still has a second, meaningfully different
	// destination: "Other (custom answer)". Keep that route discoverable.
	if optionCount > 1 {
		navigation = "↑/↓ Navigate"
	}
	canPage := window.showAbove || window.showBelow
	if navigation != "" && canPage {
		navigation += " · PgUp/PgDn Page"
	}
	primaryFooter := fmt.Sprintf("  Esc Cancel  ·  Enter Confirm  ·  %s", quickLabel)
	if !m.questionPrimaryActionReadable() {
		primaryFooter = resizeRecoveryLabel(contentWidth, "Esc Cancel")
	} else if isUnavailablePromptNotice(m.questionInputError) {
		primaryFooter = "  Esc Cancel  ·  Response unavailable"
	} else {
		primaryFooter = primaryActionFooterLabel(contentWidth, primaryFooter, "Esc Cancel", "Enter")
	}
	if m.questionPrimaryActionReadable() && navigation != "" && !canPage && lipgloss.Width(primaryFooter)+lipgloss.Width("  ·  "+navigation) <= contentWidth {
		primaryFooter += "  ·  " + navigation
	}
	builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, primaryFooter))
	if m.questionPrimaryActionReadable() && navigation != "" && canPage {
		// Paging is a real available action, not decorative metadata. Give it a
		// controlled second row instead of letting implicit wrapping decide where
		// recovery and navigation land; the frame compositor preserves footer rows.
		builder.WriteString("\n")
		builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  "+navigation))
	}
	if !compact {
		builder.WriteString("\n")
	}

	return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorSecondary)
}

type planApprovalLayout struct {
	compact, ultraCompact bool
	planDescription       string
	contractLine          string
	contractSummary       string
	showStepDescriptions  bool
	showStepsLabel        bool
	combineStepMarkers    bool
	stepRowBudget         int
}

func (m Model) buildPlanApprovalLayout(contentWidth int, bordered bool) planApprovalLayout {
	layout := planApprovalLayout{
		compact:              m.height > 0 && m.height < 24,
		ultraCompact:         m.height > 0 && m.height < 14,
		showStepDescriptions: m.height <= 0 || m.height >= 24,
		showStepsLabel:       !(m.height > 0 && m.height < 14),
		combineStepMarkers:   m.height > 0 && m.height < 14,
	}
	if m.planRequest == nil {
		return layout
	}

	if !layout.ultraCompact {
		maxDescriptionLines := 2
		if layout.compact {
			maxDescriptionLines = 1
		}
		layout.planDescription = promptWrappedText(m.planRequest.Description, max(contentWidth-2, 1), maxDescriptionLines)
	}
	if contractName := normalizeTimelineText(m.planRequest.ContractName); contractName != "" {
		layout.contractLine = "Contract: " + contractName
		if intent := normalizeTimelineText(m.planRequest.Intent); intent != "" && !layout.compact {
			layout.contractLine += " · " + intent
		}
		guards := len(m.planRequest.Boundaries) + len(m.planRequest.Invariants)
		if guards > 0 || len(m.planRequest.Examples) > 0 {
			if layout.compact {
				layout.contractLine += fmt.Sprintf(" · %d guard(s) · %d example(s)", guards, len(m.planRequest.Examples))
			} else {
				layout.contractSummary = fmt.Sprintf("%d guardrail(s) · %d example(s)", guards, len(m.planRequest.Examples))
			}
		}
	}

	if m.height <= 0 {
		layout.stepRowBudget = 14
		return layout
	}
	fixedRows := 1 // header
	if !layout.compact {
		fixedRows++
	}
	fixedRows++ // plan title
	if layout.planDescription != "" {
		fixedRows += lipgloss.Height(layout.planDescription)
	}
	if !layout.compact {
		fixedRows++
	}
	if layout.showStepsLabel && len(m.planRequest.Steps) > 0 {
		fixedRows++
	}
	if layout.contractLine != "" {
		fixedRows++
	}
	if layout.contractSummary != "" {
		fixedRows++
	}
	if !layout.compact {
		fixedRows++
	}
	fixedRows += 3 // decision rows
	if !layout.compact {
		fixedRows++
	}
	if m.planApprovalNotice != "" {
		fixedRows++
	}
	fixedRows++ // footer
	if len(m.planRequest.Steps) > 0 {
		// Paging gets its own footer row whenever the list overflows. Reserving
		// it eagerly keeps layout calculation acyclic; when all steps fit the
		// unused row simply becomes breathing room.
		fixedRows++
	}
	if bordered {
		fixedRows += 2
	}
	layout.stepRowBudget = max(m.height-fixedRows, 1)
	return layout
}

func (m Model) planStepRowCost(index int, layout planApprovalLayout) int {
	cost := 1
	if layout.showStepDescriptions && index >= 0 && index < len(m.planRequest.Steps) && normalizeTimelineText(m.planRequest.Steps[index].Description) != "" {
		cost++
	}
	return cost
}

func (m Model) planStepWindowForStart(start int, layout planApprovalLayout) viewportWindow {
	if m.planRequest == nil || len(m.planRequest.Steps) == 0 {
		return viewportWindow{}
	}
	total := len(m.planRequest.Steps)
	start = min(max(start, 0), total-1)
	budget := max(layout.stepRowBudget, 1)
	bestEnd := start + 1
	bestFound := false
	for end := start + 1; end <= total; end++ {
		cost := 0
		for i := start; i < end; i++ {
			cost += m.planStepRowCost(i, layout)
		}
		hasAbove, hasBelow := start > 0, end < total
		if hasAbove || hasBelow {
			cost++
		}
		if hasAbove && hasBelow && !layout.combineStepMarkers {
			cost++
		}
		if cost <= budget {
			bestEnd = end
			bestFound = true
		}
	}

	window := viewportWindow{start: start, end: bestEnd}
	used := m.planStepRowCost(start, layout)
	if bestFound {
		window.showAbove = start > 0
		window.showBelow = bestEnd < total
		return window
	}
	hasAbove, hasBelow := start > 0, bestEnd < total
	if hasAbove && hasBelow && layout.combineStepMarkers && used < budget {
		window.showAbove, window.showBelow = true, true
		return window
	}
	window.showAbove = hasAbove && used < budget
	if window.showAbove {
		used++
	}
	window.showBelow = hasBelow && used < budget
	return window
}

func (m Model) planStepWindow() viewportWindow {
	paletteWidth, bordered := promptPaletteWidth(m.width)
	layout := m.buildPlanApprovalLayout(max(paletteWidth-4, 1), bordered)
	start := min(max(m.planStepScroll, 0), m.planLastPageStartForLayout(layout))
	return m.planStepWindowForStart(start, layout)
}

func (m Model) planStepPageSize() int {
	window := m.planStepWindow()
	return max(window.end-window.start, 1)
}

func (m Model) planPrimaryActionReadable() bool {
	if m.planRequest == nil {
		return true
	}
	_, bordered := promptPaletteWidth(m.width)
	if m.planFeedbackMode {
		// Keep the plan title and feedback label visible with the editor; an
		// editor alone does not identify what the submitted modification targets.
		minHeight := 6 // title + label + editor + breathing row + footer + status
		layout := m.buildPlanApprovalLayout(max(promptInputContentWidth(m.width), 1), bordered)
		if layout.contractLine != "" {
			minHeight++
		}
		if safeKeyEntryText(m.planFeedbackError) != "" {
			minHeight++
		}
		if bordered {
			minHeight++
		}
		return promptPrimaryActionGeometryReadable(m.width, m.height, minHeight)
	}

	// Approval requires both its semantic target and all decision rows: plan
	// title + at least one step + three decisions + footer + global status.
	minHeight := 6 // title + three decisions + footer + global status
	if len(m.planRequest.Steps) > 0 {
		minHeight++
	}
	layout := m.buildPlanApprovalLayout(max(promptInputContentWidth(m.width), 1), bordered)
	if layout.contractLine != "" {
		minHeight++
	}
	if safeKeyEntryText(m.planApprovalNotice) != "" {
		minHeight++
	}
	if m.planStepsCanPage() {
		minHeight++
	}
	if bordered {
		minHeight++
	}
	return promptPrimaryActionGeometryReadable(m.width, m.height, minHeight)
}

func (m Model) planLastPageStart() int {
	if m.planRequest == nil || len(m.planRequest.Steps) == 0 {
		return 0
	}
	paletteWidth, bordered := promptPaletteWidth(m.width)
	layout := m.buildPlanApprovalLayout(max(paletteWidth-4, 1), bordered)
	return m.planLastPageStartForLayout(layout)
}

func (m Model) planLastPageStartForLayout(layout planApprovalLayout) int {
	if m.planRequest == nil || len(m.planRequest.Steps) == 0 {
		return 0
	}
	total := len(m.planRequest.Steps)
	for start := 0; start < total; start++ {
		if m.planStepWindowForStart(start, layout).end == total {
			return start
		}
	}
	return total - 1
}

// renderPlanApproval renders the plan approval UI.
func (m Model) renderPlanApproval() string {
	if m.planRequest == nil {
		return ""
	}

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(paletteWidth-4, 1)
	layout := m.buildPlanApprovalLayout(contentWidth, bordered)
	compact := layout.compact

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
	if layout.planDescription != "" {
		b.WriteString("  " + descStyle.Render(layout.planDescription) + "\n")
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
			renderStart := min(max(m.planStepScroll, 0), m.planLastPageStartForLayout(layout))
			window := m.planStepWindowForStart(renderStart, layout)
			start, end := window.start, window.end
			if layout.showStepsLabel {
				b.WriteString("  " + labelStyle.Render("Steps:") + "\n")
			}
			if layout.combineStepMarkers && window.showAbove && window.showBelow {
				marker := truncateForWidth(fmt.Sprintf("↑ %d earlier · ↓ %d more", start, len(m.planRequest.Steps)-end), max(contentWidth-2, 1))
				b.WriteString("  " + subtitleStyle.Render(marker) + "\n")
			} else if window.showAbove {
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
				if i == end-1 && end < len(m.planRequest.Steps) && !window.showBelow {
					// With a one-row step budget there is no room for a separate
					// disclosure marker. Keep the familiar label when the row is wide
					// enough, and use a compact positional prefix when it is not.
					position := fmt.Sprintf("%d/%d", i+1, len(m.planRequest.Steps))
					verbose := line + " · " + position
					if lipgloss.Width(verbose) <= max(contentWidth-4, 1) {
						line = verbose
					} else {
						line = position + " " + stepTitle
					}
				}
				b.WriteString("  " + stepNumStyle.Render("○") + " " + stepTitleStyle.Render(truncateForWidth(line, max(contentWidth-4, 1))) + "\n")
				if layout.showStepDescriptions {
					if sd := normalizeTimelineText(step.Description); sd != "" {
						b.WriteString("     " + stepDescStyle.Render(truncateForWidth(sd, max(contentWidth-5, 1))) + "\n")
					}
				}
			}
			if window.showBelow && !(layout.combineStepMarkers && window.showAbove) {
				marker := truncateForWidth(fmt.Sprintf("↓ %d more step(s)", len(m.planRequest.Steps)-end), max(contentWidth-2, 1))
				b.WriteString("  " + subtitleStyle.Render(marker) + "\n")
			}
		}
	}

	if layout.contractLine != "" {
		contract := truncateForWidth(layout.contractLine, max(contentWidth-2, 1))
		b.WriteString("  " + lipgloss.NewStyle().Foreground(ColorAccent).Render(contract) + "\n")
		if layout.contractSummary != "" {
			summary := truncateForWidth(layout.contractSummary, max(contentWidth-2, 1))
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
		if !m.planPrimaryActionReadable() {
			feedbackFooter = resizeRecoveryLabel(contentWidth, "Esc Back")
		} else if isUnavailablePromptNotice(m.planFeedbackError) {
			feedbackFooter = "  Esc Back  ·  Submission unavailable"
		} else {
			feedbackFooter = primaryActionFooterLabel(contentWidth, feedbackFooter, "Esc Back", "Enter")
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
	if !m.planPrimaryActionReadable() {
		footer = resizeRecoveryLabel(contentWidth, "Esc Cancel")
	} else if isUnavailablePromptNotice(m.planApprovalNotice) {
		footer = "  Esc Cancel  ·  Response unavailable"
	} else if len(m.planRequest.Steps) == 0 {
		footer = "  Esc Cancel  ·  Enter Confirm  ·  2-3 Select  ·  ↑/↓ Choose"
	} else {
		footer = primaryActionFooterLabel(contentWidth, footer, "Esc Cancel", "Enter")
	}
	b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))
	if m.planPrimaryActionReadable() && m.planStepsCanPage() {
		b.WriteString("\n")
		b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "  PgUp/PgDn Steps  ·  Home/End Jump"))
	}

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPlan)
}

// renderModelSelector renders the model selector UI.
func (m Model) renderModelSelector() string {
	var builder strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(paletteWidth-4, 1)
	layout := m.buildModelSelectorLayout(bordered)
	compact := layout.compact

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
	start, end := layout.window.start, layout.window.end

	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	if layout.window.showAbove {
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

		if layout.showDescriptions {
			// Muted description
			description := safeKeyEntryText(model.Description)
			if description == "" {
				description = "No description provided."
			}
			fmt.Fprintf(&builder, "     %s\n", m.styles.ModalMuted.Render(truncateForWidth(description, max(contentWidth-5, 1))))
		}
	}
	if layout.window.showBelow {
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
	selected, _ := m.selectedModelChoice()
	footer := "  Esc " + escapeAction
	switch {
	case !m.modelSelectorPrimaryActionReadable():
		footer = resizeRecoveryLabel(contentWidth, "Esc "+map[bool]string{true: "Back", false: "Close"}[m.modelSelectorReturnState == StateSettings])
	case m.modelSwitchPending != "":
		footer += " · Applying…"
	case selected.ID == "":
		footer += " · Selected unavailable"
	case selected.ID == safeKeyEntryText(m.currentModel):
		footer += " · Enter Keep current"
	case !m.modelSelectAvailable():
		footer += " · Read-only"
	default:
		footer += " · Enter Confirm"
	}
	quickMax := min(9, len(m.availableModels))
	quickSelectable := quickMax > 1 && m.modelSwitchPending == "" && m.modelSelectAvailable()
	for i := 0; i < quickMax && quickSelectable; i++ {
		quickSelectable = safeKeyEntryText(m.availableModels[i].ID) != ""
	}
	if quickSelectable && m.modelSelectorPrimaryActionReadable() {
		footer += fmt.Sprintf(" · 1-%d Select", quickMax)
	}
	if len(m.availableModels) > 1 && m.modelSelectorPrimaryActionReadable() {
		footer += " · ↑/↓ Navigate"
	}
	if m.modelSelectorPrimaryActionReadable() && m.modelSwitchPending == "" && selected.ID != "" &&
		(selected.ID == safeKeyEntryText(m.currentModel) || m.modelSelectAvailable()) {
		recovery := "Esc Close"
		if m.modelSelectorReturnState == StateSettings {
			recovery = "Esc Back"
		}
		footer = primaryActionFooterLabel(contentWidth, footer, recovery, "Enter")
	}
	builder.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))
	builder.WriteString("\n")

	return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorPrimary)
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
