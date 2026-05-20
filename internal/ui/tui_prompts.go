package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// promptPaletteWidth returns the inner content width for prompt cards and
// whether the card should render with a rounded border + fixed width.
// On narrow terminals (width < minBorderedPromptWidth) we skip the border
// entirely and let the content flow at the terminal's natural width — a
// 45-cell bordered container in a 40-cell terminal overflows and breaks
// the layout horizontally, which is worse than a borderless prompt.
func promptPaletteWidth(termWidth int) (width int, bordered bool) {
	if termWidth < minBorderedPromptWidth {
		// Fall back: no border, use whatever width we have (minimum 30 so
		// truncation budgets stay sane).
		w := termWidth - 4 // 2-space left padding + 2-space right margin
		if w < 30 {
			w = 30
		}
		return w, false
	}
	w := min(78, termWidth-6)
	if w < minBorderedPromptWidth {
		w = minBorderedPromptWidth
	}
	return w, true
}

// minBorderedPromptWidth is the threshold below which prompt cards drop
// the rounded border and fixed Width(). Set to give the 45-wide content
// + 2 border cells + 2 padding cells + a small right margin room to
// breathe; below this the bordered look just collides with the edge.
const minBorderedPromptWidth = 50

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

	// Border color based on risk level
	borderColor := ColorWarning
	if m.permRequest.RiskLevel == "high" {
		borderColor = ColorError
	} else if m.permRequest.RiskLevel == "low" {
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
	switch m.permRequest.RiskLevel {
	case "high":
		riskBadge = lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render("HIGH RISK")
	case "medium":
		riskBadge = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("MEDIUM RISK")
	default:
		riskBadge = lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true).Render("LOW RISK")
	}

	var builder strings.Builder

	// Title line with badge
	builder.WriteString(titleStyle.Render("Permission Required"))
	builder.WriteString("  ")
	builder.WriteString(riskBadge)
	builder.WriteString("\n\n")

	// Tool info with operation context
	opLabel := formatPermissionOperation(m.permRequest.ToolName, m.permRequest.Args)
	builder.WriteString(markerStyle.Render("  ▸ ") + labelStyle.Render("Tool: ") + valueStyle.Render(opLabel))
	builder.WriteString("\n")

	// Command/Details — show the key argument value
	if len(m.permRequest.Args) > 0 {
		detail := ""
		detailBudget := max(paletteWidth-10, 30)
		for _, key := range []string{"command", "file_path", "path", "pattern", "url"} {
			if val, ok := m.permRequest.Args[key].(string); ok && val != "" {
				if runes := []rune(val); len(runes) > detailBudget {
					val = string(runes[:detailBudget-3]) + "..."
				}
				detail = val
				break
			}
		}
		if detail != "" {
			builder.WriteString(markerStyle.Render("    ") + valueStyle.Render(detail))
			builder.WriteString("\n")
		}
	}

	// Reason (compact)
	if m.permRequest.Reason != "" {
		reason := m.permRequest.Reason
		reasonBudget := max(paletteWidth-10, 30)
		if runes := []rune(reason); len(runes) > reasonBudget {
			reason = string(runes[:reasonBudget-3]) + "..."
		}
		builder.WriteString(markerStyle.Render("    ") + labelStyle.Render(reason))
		builder.WriteString("\n")
	}

	builder.WriteString("\n")

	// Inline options — single line
	optionStyle := lipgloss.NewStyle().Foreground(ColorDim)
	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)

	options := []struct {
		key   string
		label string
	}{
		{"y", "Allow"},
		{"a", "Always"},
		{"n", "Deny"},
	}

	var optParts []string
	for i, opt := range options {
		style := optionStyle
		if i == m.permSelectedOption {
			style = selectedStyle
		}
		optParts = append(optParts, style.Render(opt.key+" "+opt.label))
	}

	builder.WriteString("  " + strings.Join(optParts, optionStyle.Render(" · ")) + optionStyle.Render(" · esc Cancel"))
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

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary)

	// Header
	builder.WriteString(titleStyle.Render("Question from Agent"))
	builder.WriteString("\n\n")

	// Question text (wrap nicely)
	questionStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Width(paletteWidth - 4)
	builder.WriteString(questionStyle.Render(m.questionRequest.Question))
	builder.WriteString("\n\n")

	// If custom input mode or no options, show input
	if m.questionCustomInput || len(m.questionRequest.Options) == 0 {
		builder.WriteString("  " + m.questionInputModel.View())
		builder.WriteString("\n\n")

		footerStyle := lipgloss.NewStyle().
			Foreground(ColorDim).
			Italic(true).
			Align(lipgloss.Center).
			Width(paletteWidth - 4)

		if m.questionCustomInput {
			builder.WriteString(footerStyle.Render("Enter to submit  ·  Esc Go Back"))
		} else {
			builder.WriteString(footerStyle.Render("Type answer  ·  Enter Confirm"))
		}
		builder.WriteString("\n")
		return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorSecondary)
	}

	// Options - using modal styles
	for i, opt := range m.questionRequest.Options {
		prefix := "  "
		style := m.styles.ModalNormal
		if i == m.questionSelectedOption {
			prefix = "> "
			style = m.styles.ModalSelected
		}

		label := fmt.Sprintf("%d. %s", i+1, opt)
		if opt == m.questionRequest.Default {
			label += " " + m.styles.ModalDefault.Render("(default)")
		}
		fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render(label))
	}

	// "Other" option
	otherIdx := len(m.questionRequest.Options)
	prefix := "  "
	style := m.styles.ModalNormal
	if m.questionSelectedOption == otherIdx {
		prefix = "> "
		style = m.styles.ModalSelected
	}
	fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render("Other (custom answer)"))

	builder.WriteString("\n")

	footerStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true).
		Align(lipgloss.Center).
		Width(paletteWidth - 4)
	builder.WriteString(footerStyle.Render("↑/↓ Navigate  ·  Enter Confirm  ·  Esc Close"))
	builder.WriteString("\n")

	return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorSecondary)
}

// renderPlanApproval renders the plan approval UI.
func (m Model) renderPlanApproval() string {
	if m.planRequest == nil {
		return ""
	}

	var builder strings.Builder

	// Styles
	borderStyle := lipgloss.NewStyle().Foreground(ColorPlan)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan)
	planTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	descStyle := lipgloss.NewStyle().Foreground(ColorMuted).Italic(true)
	stepNumStyle := lipgloss.NewStyle().Foreground(ColorDim)
	stepTitleStyle := lipgloss.NewStyle().Foreground(ColorText)
	stepDescStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	infoStyle := lipgloss.NewStyle().Foreground(ColorInfo)

	// Calculate width
	panelWidth := m.width - 4
	if panelWidth < 40 {
		panelWidth = m.width // Full width on very narrow terminals
	}
	if panelWidth > 90 {
		panelWidth = 90
	}

	// Header
	stepCount := len(m.planRequest.Steps)
	headerInfo := fmt.Sprintf(" %d steps ", stepCount)
	headerTitle := " Plan Approval "

	// Calculate width using lipgloss.Width for styled text
	styledTitleWidth := lipgloss.Width(titleStyle.Render(headerTitle))
	styledInfoWidth := lipgloss.Width(infoStyle.Render(headerInfo))
	headerDashCount := panelWidth - styledTitleWidth - styledInfoWidth - 4
	if headerDashCount < 0 {
		headerDashCount = 0
	}

	builder.WriteString(borderStyle.Render("╭─"))
	builder.WriteString(titleStyle.Render(headerTitle))
	builder.WriteString(borderStyle.Render(strings.Repeat("─", headerDashCount)))
	builder.WriteString(infoStyle.Render(headerInfo))
	builder.WriteString(borderStyle.Render("─╮"))
	builder.WriteString("\n")

	// Plan title
	titleLine := "  " + planTitleStyle.Render(m.planRequest.Title)
	builder.WriteString(borderStyle.Render("│"))
	builder.WriteString(titleLine)
	padding := panelWidth - 1 - lipgloss.Width(titleLine)
	if padding > 0 {
		builder.WriteString(strings.Repeat(" ", padding))
	}
	builder.WriteString(borderStyle.Render("│"))
	builder.WriteString("\n")

	// Description (if present)
	if m.planRequest.Description != "" {
		desc := m.planRequest.Description
		if lipgloss.Width(desc) > panelWidth-6 {
			// Truncate by visual width, not rune count (handles CJK/emoji correctly)
			runes := []rune(desc)
			for i := len(runes); i > 0; i-- {
				candidate := string(runes[:i]) + "..."
				if lipgloss.Width(candidate) <= panelWidth-6 {
					desc = candidate
					break
				}
			}
		}
		descLine := "  " + descStyle.Render(desc)
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(descLine)
		padding := panelWidth - 1 - lipgloss.Width(descLine)
		if padding > 0 {
			builder.WriteString(strings.Repeat(" ", padding))
		}
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")
	}

	// Steps header
	stepsHeader := "  Steps:"
	stepsHeaderPadding := panelWidth - 1 - lipgloss.Width(infoStyle.Render(stepsHeader))
	if stepsHeaderPadding < 0 {
		stepsHeaderPadding = 0
	}
	builder.WriteString(borderStyle.Render("│"))
	builder.WriteString(infoStyle.Render(stepsHeader))
	builder.WriteString(strings.Repeat(" ", stepsHeaderPadding))
	builder.WriteString(borderStyle.Render("│"))
	builder.WriteString("\n")

	// Steps tree view
	for _, step := range m.planRequest.Steps {
		// Step icon: all pending in approval view
		stepIcon := "○"
		stepLine := "  " + stepNumStyle.Render(stepIcon) + " " + stepTitleStyle.Render(fmt.Sprintf("Step %d: %s", step.ID, step.Title))

		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(stepLine)
		padding := panelWidth - 1 - lipgloss.Width(stepLine)
		if padding > 0 {
			builder.WriteString(strings.Repeat(" ", padding))
		}
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")

		// Step description (truncated)
		if step.Description != "" {
			desc := step.Description
			maxDescLen := panelWidth - 10
			if lipgloss.Width(desc) > maxDescLen {
				runes := []rune(desc)
				for i := len(runes); i > 0; i-- {
					candidate := string(runes[:i]) + "..."
					if lipgloss.Width(candidate) <= maxDescLen {
						desc = candidate
						break
					}
				}
			}
			descLine := "     " + stepDescStyle.Render(desc)
			builder.WriteString(borderStyle.Render("│"))
			builder.WriteString(descLine)
			padding := panelWidth - 1 - lipgloss.Width(descLine)
			if padding > 0 {
				builder.WriteString(strings.Repeat(" ", padding))
			}
			builder.WriteString(borderStyle.Render("│"))
			builder.WriteString("\n")
		}
	}

	// Contract info (if present)
	if m.planRequest.ContractName != "" {
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(strings.Repeat(" ", panelWidth-1))
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")

		contractLine := "  Contract: " + m.planRequest.ContractName
		styledContractLine := lipgloss.NewStyle().Foreground(ColorAccent).Render(contractLine)
		contractPadding := panelWidth - 1 - lipgloss.Width(styledContractLine)
		if contractPadding < 0 {
			contractPadding = 0
		}
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(styledContractLine)
		builder.WriteString(strings.Repeat(" ", contractPadding))
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")
	}

	// Empty line before options
	builder.WriteString(borderStyle.Render("│"))
	builder.WriteString(strings.Repeat(" ", panelWidth-1))
	builder.WriteString(borderStyle.Render("│"))
	builder.WriteString("\n")

	// If in feedback mode, show feedback input
	if m.planFeedbackMode {
		feedbackLine := "  Enter your feedback:"
		styledFeedbackLine := infoStyle.Bold(true).Render(feedbackLine)
		feedbackPadding := panelWidth - 1 - lipgloss.Width(styledFeedbackLine)
		if feedbackPadding < 0 {
			feedbackPadding = 0
		}
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(styledFeedbackLine)
		builder.WriteString(strings.Repeat(" ", feedbackPadding))
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")

		inputLine := "  " + m.planFeedbackInput.View()
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(inputLine)
		padding := panelWidth - 1 - lipgloss.Width(inputLine)
		if padding > 0 {
			builder.WriteString(strings.Repeat(" ", padding))
		}
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")

		// Footer
		builder.WriteString(borderStyle.Render("╰" + strings.Repeat("─", panelWidth-1) + "╯"))
		builder.WriteString("\n")
		builder.WriteString(m.styles.StatusBar.Render("  Enter to submit • Esc to cancel"))
		return builder.String()
	}

	// Options section
	options := []struct {
		key  string
		text string
		icon string
	}{
		{"y", "Approve", "✓"},
		{"n", "Reject", "✗"},
		{"m", "Request changes", "✎"},
	}

	selectedStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan).Background(ColorBorder)
	normalStyle := lipgloss.NewStyle().Foreground(ColorText)
	keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)

	for i, opt := range options {
		prefix := "    "
		style := normalStyle
		icon := lipgloss.NewStyle().Foreground(ColorDim).Render(opt.icon)
		if i == m.planSelectedOption {
			prefix = "  > "
			style = selectedStyle
			icon = lipgloss.NewStyle().Foreground(ColorPlan).Bold(true).Render(opt.icon)
		}

		optLine := prefix + icon + " " + style.Render(opt.text) + " " + keyStyle.Render("("+opt.key+")")
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString(optLine)
		padding := panelWidth - 1 - lipgloss.Width(optLine)
		if padding > 0 {
			builder.WriteString(strings.Repeat(" ", padding))
		}
		builder.WriteString(borderStyle.Render("│"))
		builder.WriteString("\n")
	}

	// Footer
	builder.WriteString(borderStyle.Render("╰" + strings.Repeat("─", panelWidth-1) + "╯"))
	builder.WriteString("\n")
	builder.WriteString(lipgloss.NewStyle().Foreground(ColorDim).Render("  ↑↓ Navigate • Enter Confirm • y/n/m Quick action"))

	return builder.String()
}

// renderModelSelector renders the model selector UI.
func (m Model) renderModelSelector() string {
	var builder strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary)

	subtitleStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	// Header
	builder.WriteString(titleStyle.Render("Select Model"))
	builder.WriteString("  ")
	builder.WriteString(subtitleStyle.Render("Ctrl+K"))
	builder.WriteString("\n\n")

	// Current model info
	fmt.Fprintf(&builder, "  Current: %s\n\n", m.styles.Spinner.Render(m.currentModel))

	if len(m.availableModels) == 0 {
		emptyStyle := lipgloss.NewStyle().
			Foreground(ColorMuted).
			Italic(true).
			Width(paletteWidth - 4)
		builder.WriteString(emptyStyle.Render("  No model choices are loaded for this session. Use /model to inspect or set the active model."))
		builder.WriteString("\n\n")
		return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorPrimary)
	}

	// Model options - using modal styles
	for i, model := range m.availableModels {
		prefix := "  "
		style := m.styles.ModalNormal
		if i == m.modelSelectedIndex {
			prefix = "> "
			style = m.styles.ModalSelected
		}

		// Show number for quick select
		label := fmt.Sprintf("%d. %s", i+1, model.Name)
		if model.ID == m.currentModel {
			label += " " + m.styles.ModalDefault.Render("(current)")
		}
		fmt.Fprintf(&builder, "%s%s\n", prefix, style.Render(label))

		// Muted description
		descStyle := m.styles.ModalMuted.Width(paletteWidth - 8)
		fmt.Fprintf(&builder, "     %s\n", descStyle.Render(model.Description))
	}

	builder.WriteString("\n")

	footerStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true).
		Align(lipgloss.Center).
		Width(paletteWidth - 4)
	builder.WriteString(footerStyle.Render("↑/↓ Navigate  ·  Enter Confirm  ·  Esc Close"))
	builder.WriteString("\n")

	return wrapPromptContainer(builder.String(), paletteWidth, bordered, ColorPrimary)
}

// renderShortcutsOverlay renders the keyboard shortcuts overlay.
func (m Model) renderShortcutsOverlay() string {
	if m.shortcutsOverlay != nil && m.shortcutsOverlay.IsVisible() {
		return m.shortcutsOverlay.View(m.width, m.height)
	}

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Padding(0, 1)

	categoryStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent).
		MarginTop(1)

	keyStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Width(16)

	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Width(50)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1, 2)

	var builder strings.Builder

	builder.WriteString(titleStyle.Render("  Keyboard Shortcuts"))
	builder.WriteString("\n")

	// Input
	builder.WriteString(categoryStyle.Render("Input"))
	builder.WriteString("\n")
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Enter"), descStyle.Render("Send message"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("?"), descStyle.Render("Show this help"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/"), descStyle.Render("Browse command history"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Tab"), descStyle.Render("Autocomplete commands & files"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+U"), descStyle.Render("Clear input line"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+R"), descStyle.Render("Search history (reverse)"))

	// Navigation
	builder.WriteString(categoryStyle.Render("Navigation"))
	builder.WriteString("\n")
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("PgUp/PgDn"), descStyle.Render("Scroll history"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+U"), descStyle.Render("Half page up when input is empty"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+D"), descStyle.Render("Half page down when input is empty"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+L"), descStyle.Render("Clear / Redraw"))

	// Code Blocks
	builder.WriteString(categoryStyle.Render("Code Blocks"))
	builder.WriteString("\n")
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("[ / ]"), descStyle.Render("Previous/Next block"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Tab"), descStyle.Render("Apply code block"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("c"), descStyle.Render("Copy selected block"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("y"), descStyle.Render("Copy last AI response"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Shift+Y"), descStyle.Render("Copy chat history"))

	// Command Center
	builder.WriteString(categoryStyle.Render("Command Center"))
	builder.WriteString("\n")
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+P"), descStyle.Render("Command Palette (All Actions)"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+K"), descStyle.Render("Open model selector"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+E"), descStyle.Render("Expand / collapse last tool output"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Shift+Tab"), descStyle.Render("Cycle Normal / Plan / YOLO"))

	// Slash Commands
	builder.WriteString(categoryStyle.Render("Slash Commands"))
	builder.WriteString("\n")
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/help"), descStyle.Render("Show all available commands"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/clear"), descStyle.Render("Clear conversation history"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/save"), descStyle.Render("Save current session"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/sessions"), descStyle.Render("List saved sessions"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/model"), descStyle.Render("Switch AI model"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/cost"), descStyle.Render("Show token usage & costs"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/browse"), descStyle.Render("Browse project files"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/git-status"), descStyle.Render("Show git status"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/commit"), descStyle.Render("Create git commit"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/checkpoint"), descStyle.Render("Create checkpoint"))
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("/doctor"), descStyle.Render("Diagnose issues"))

	// Session
	builder.WriteString(categoryStyle.Render("Session"))
	builder.WriteString("\n")
	fmt.Fprintf(&builder, "  %s%s\n", keyStyle.Render("Ctrl+C"), descStyle.Render("Cancel once, quit on second press"))

	builder.WriteString("\n")
	builder.WriteString(m.styles.StatusBar.Render("Press any key to close"))

	return boxStyle.Render(builder.String())
}
