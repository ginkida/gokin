package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ansiCSIPattern matches Control Sequence Introducer escapes — SGR (colour
// resets, foregrounds, backgrounds) and friends. Used by isVisuallyBlank
// to recognise lines that carry only formatting escapes and would render
// as blank rows in the terminal.
var ansiCSIPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// isVisuallyBlank reports whether a line renders as empty after the
// terminal strips ANSI escapes. Lines like "\x1b[0m" — emitted by syntax
// highlighters around source blank lines — pass `s == ""` but visually
// occupy a blank row inside a card body; this catches them.
func isVisuallyBlank(s string) bool {
	return strings.TrimSpace(ansiCSIPattern.ReplaceAllString(s, "")) == ""
}

// getTextSelectionHint returns a platform/terminal-specific hint for text selection.
func getTextSelectionHint() string {
	term := os.Getenv("TERM_PROGRAM")
	termEnv := os.Getenv("TERM")

	switch {
	case runtime.GOOS == "darwin" && term == "iTerm.app":
		return "Hold ⌥+Drag to select text"
	case runtime.GOOS == "darwin" && term == "Apple_Terminal":
		return "Hold Fn+Drag to select text"
	case term == "WezTerm":
		return "Hold Shift+Drag to select text"
	case strings.Contains(termEnv, "kitty") || term == "kitty":
		return "Hold Shift+Drag to select text"
	case term == "Alacritty" || strings.Contains(termEnv, "alacritty"):
		return "Hold Shift+Drag to select text"
	case runtime.GOOS == "darwin":
		return "Hold ⌥+Drag (iTerm) or Fn+Drag (Terminal) to select"
	default:
		return "Hold Shift+Drag to select text"
	}
}

// copyViaOSC52 copies text to clipboard via OSC 52 escape sequence.
func copyViaOSC52(text string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stderr, "\033]52;c;%s\a", encoded)
}

// getMacOSBattery removed as battery monitoring is disabled.

// SetBadgeCmd returns a tea.Cmd that sets the iTerm2 badge.
// Uses stderr to avoid corrupting Bubble Tea's stdout rendering.
func SetBadgeCmd(badge string) tea.Cmd {
	return func() tea.Msg {
		if os.Getenv("TERM_PROGRAM") == "iTerm.app" {
			encoded := base64.StdEncoding.EncodeToString([]byte(badge))
			fmt.Fprintf(os.Stderr, "\033]1337;SetBadgeFormat=%s\a", encoded)
		}
		return nil
	}
}

// ClearBadgeCmd returns a tea.Cmd that clears the iTerm2 badge.
// Uses stderr to avoid corrupting Bubble Tea's stdout rendering.
func ClearBadgeCmd() tea.Cmd {
	return func() tea.Msg {
		if os.Getenv("TERM_PROGRAM") == "iTerm.app" {
			fmt.Fprintf(os.Stderr, "\033]1337;SetBadgeFormat=\a")
		}
		return nil
	}
}

// setTerminalTitle sets the terminal window title via OSC-0 escape sequence.
// Visible in terminal tabs, tmux, iTerm2, etc.
func setTerminalTitle(title string) {
	fmt.Fprintf(os.Stderr, "\033]0;%s\a", title)
}

// Welcome displays a minimalist welcome message using lipgloss borders.
//
// What lands here matters disproportionately: it's the first thing the
// user reads after typing `gokin`. We surface three things on this
// screen and nothing else: WHO they're talking to (model + path),
// WHAT mode they're in (so they're not surprised by plan-mode
// approval prompts), and HOW to do common things (Ctrl+P / Shift+Tab /
// slash). Quick-start suggestions sit just outside the box so users
// who already know the app can skim past them.
func (m *Model) Welcome() {
	titleStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	infoStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	accentStyle := lipgloss.NewStyle().Foreground(ColorSecondary)

	dir := shortenPath(prettyPath(m.workDir), 30)

	modelName := m.currentModel
	if modelName == "" {
		modelName = "no model"
	}

	setTerminalTitle(fmt.Sprintf("Gokin · %s · %s", modelName, dir))

	// Mode badge — the most important state signal on the welcome
	// banner. A user dropped into plan mode by default needs to see
	// "✦ plan mode" prominently or they'll be surprised when their
	// first request triggers an approval flow. Each mode gets a
	// distinct color hue (info-blue / warn-amber / dim-default) that
	// matches the status bar so the two surfaces feel consistent.
	modeBadge := m.welcomeModeBadge()

	line1 := titleStyle.Render("GOKIN")
	line2 := infoStyle.Render(dir) + dimStyle.Render(" · ") + accentStyle.Render(modelName)
	line3 := modeBadge
	line4 := dimStyle.Render("Ctrl+P") + infoStyle.Render(" commands") +
		dimStyle.Render("  ·  Shift+Tab") + infoStyle.Render(" cycle mode") +
		dimStyle.Render("  ·  /") + infoStyle.Render(" explore")

	content := line1 + "\n" + line2 + "\n" + line3 + "\n" + line4

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(1, 3).
		Align(lipgloss.Center)

	m.output.AppendLine(boxStyle.Render(content))
	m.output.AppendLine("")

	// Quick-start hints stay outside the box — visual hierarchy puts
	// the box first (state), suggestions second (actions). User
	// instinctively reads top-to-bottom.
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	promptStyle := lipgloss.NewStyle().Foreground(ColorAccent).Italic(true)
	suggestions := suggestionStyle.Render("  Quick start: ") +
		promptStyle.Render("\"explain this codebase\"") +
		suggestionStyle.Render("  ·  ") +
		promptStyle.Render("\"fix the failing test\"") +
		suggestionStyle.Render("  ·  ") +
		promptStyle.Render("/help")
	m.output.AppendLine(suggestions)
}

// welcomeModeBadge renders the current session-mode indicator for the
// welcome banner. Lives separately from the status-bar version because
// the banner has more vertical real-estate and benefits from a short
// human-readable description right next to the badge — first-time users
// shouldn't have to learn what "plan mode" means by hitting it
// experimentally.
func (m *Model) welcomeModeBadge() string {
	planStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	yoloStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(ColorDim)
	hintStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	switch {
	case m.planningModeEnabled:
		return planStyle.Render("✦ plan mode") + " " +
			hintStyle.Render("(read-only · proposes plan before acting)")
	case !m.permissionsEnabled || !m.sandboxEnabled:
		return yoloStyle.Render("⚠ YOLO mode") + " " +
			hintStyle.Render("(no prompts · agent runs everything)")
	default:
		return normalStyle.Render("● normal mode") + " " +
			hintStyle.Render("(asks before write/edit/bash)")
	}
}

// AddSystemMessage adds a system message to the output.
func (m *Model) AddSystemMessage(msg string) {
	iconStyle := lipgloss.NewStyle().Foreground(ColorDim)
	// Quiet dim notice (was bold saturated teal) — a transient "switched model"
	// note should recede, not out-shout the model's prose.
	textStyle := lipgloss.NewStyle().
		Foreground(ColorMuted).
		MarginBottom(1)
	m.output.AppendLine(iconStyle.Render("  ▸ ") + textStyle.Render(msg))
}

// renderTodos renders the current todo items.
func (m Model) renderTodos() string {
	if len(m.todoItems) == 0 {
		return ""
	}

	// Style for the todo box - Enhanced
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGradient2).
		Padding(0, 1).
		MarginBottom(1)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	itemStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		PaddingLeft(1)

	var builder strings.Builder
	builder.WriteString(titleStyle.Render(" Tasks"))
	builder.WriteString("\n")

	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	idx := int(time.Now().UnixNano()/100000000) % len(spinner)
	spinChar := spinner[idx]

	// Long lists collapse completed items into one summary row: a 15-step
	// plan otherwise renders a 17-line box where most rows are ✓ history.
	// Short lists (≤ todosPanelFullRender) keep the full picture.
	collapseDone := len(m.todoItems) > todosPanelFullRender
	doneCount := 0
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	for _, item := range m.todoItems {
		text := strings.TrimSpace(item)
		if strings.HasPrefix(text, "- ") {
			text = strings.TrimSpace(text[2:])
		}

		var styledItem string
		if strings.HasPrefix(text, "[ ]") {
			content := strings.TrimSpace(text[3:])
			styledItem = m.styles.TodoPending.Render("○ " + content)
		} else if strings.HasPrefix(text, "[/]") {
			content := strings.TrimSpace(text[3:])
			styledItem = m.styles.TodoActive.Render(spinChar + " " + content)
		} else if strings.HasPrefix(text, "[x]") || strings.HasPrefix(text, "[X]") {
			if collapseDone {
				doneCount++
				continue
			}
			content := strings.TrimSpace(text[3:])
			styledItem = m.styles.TodoDone.Render("✓ " + content)
		} else {
			// Fallback if no known prefix
			styledItem = itemStyle.Render("• " + text)
		}

		builder.WriteString(m.styles.TodoItem.Render(styledItem))
		builder.WriteString("\n")
	}

	if doneCount > 0 {
		builder.WriteString(m.styles.TodoItem.Render(dimStyle.Render(fmt.Sprintf("✓ %d completed", doneCount))))
		builder.WriteString("\n")
	}

	return boxStyle.Render(strings.TrimSuffix(builder.String(), "\n"))
}

// todosPanelFullRender is the largest todo list that still renders every
// item, including completed ones. Above it, ✓ items collapse to one
// "✓ N completed" row — the pending/active rows are the signal.
const todosPanelFullRender = 8

// renderScratchpad renders the agent scratchpad.
func (m Model) renderScratchpad() string {
	if m.scratchpad == "" {
		return ""
	}

	// Style for the scratchpad box - Distinct from tasks
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Padding(0, 1).
		MarginBottom(1).
		Width(m.width - 4)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary)

	contentStyle := lipgloss.NewStyle().
		Foreground(ColorText)

	var builder strings.Builder
	builder.WriteString(titleStyle.Render(" Scratchpad"))
	builder.WriteString("\n")

	// Render only the TAIL of the scratchpad — it's append-style working
	// notes, so the latest lines carry the signal. Unbounded render let a
	// chatty agent grow the box to half the screen.
	lines := strings.Split(strings.TrimRight(m.scratchpad, "\n"), "\n")
	if hidden := len(lines) - scratchpadMaxRenderLines; hidden > 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
		builder.WriteString(dimStyle.Render(fmt.Sprintf("… %d earlier line(s)", hidden)))
		builder.WriteString("\n")
		lines = lines[hidden:]
	}
	builder.WriteString(contentStyle.Render(strings.Join(lines, "\n")))

	return boxStyle.Render(builder.String())
}

// scratchpadMaxRenderLines caps the scratchpad box height (content lines,
// excluding the title and the "… earlier" marker).
const scratchpadMaxRenderLines = 6

// getCommandHint returns a hint for the current command input.
func (m Model) getCommandHint(input string) string {
	// Extract command name
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return ""
	}

	cmd := strings.TrimPrefix(parts[0], "/")

	// Command hints map
	hints := map[string]string{
		"help":              "Show all available commands and their usage",
		"quickstart":        "Run interactive onboarding and key workflows",
		"clear":             "Clear the current conversation history",
		"compact":           "Summarize old context to free up token window",
		"save":              "Save the current session to disk",
		"resume":            "Resume a previously saved session",
		"sessions":          "List all saved sessions",
		"stats":             "Show session stats, usage and health snapshot",
		"instructions":      "Show project-specific instructions (GOKIN/CLAUDE/.gokin)",
		"commit":            "Create a git commit with AI-generated message",
		"pr":                "Create a pull request",
		"checkpoint":        "Save a checkpoint of the current state",
		"checkpoints":       "List all saved checkpoints",
		"restore":           "Restore a previously saved checkpoint",
		"init":              "Initialize project configuration",
		"doctor":            "Run diagnostics to check for issues",
		"config":            "Show or edit configuration",
		"status":            "Show provider/auth/runtime configuration status",
		"login":             "Set API key for a provider",
		"logout":            "Remove provider key or logout from all",
		"provider":          "Switch active AI provider",
		"update":            "Check/install updates, list backups, rollback",
		"cost":              "Show token usage and estimated costs",
		"model":             "Switch AI model",
		"reasoning":         "Set reasoning effort (none/low/medium/high/xhigh)",
		"browse":            "Browse project files",
		"open":              "Open a file in your editor",
		"ql":                "Quick preview file contents",
		"git-status":        "Show git repository status",
		"clear-todos":       "Clear the todo list",
		"plan":              "Toggle planning mode (or press Shift+Tab)",
		"resume-plan":       "Resume paused or saved plan execution",
		"copy":              "Copy text, --last for AI response, --all for full chat",
		"paste":             "Paste from clipboard",
		"permissions":       "Toggle or inspect command permission prompts",
		"sandbox":           "Toggle sandbox mode for shell tool execution",
		"theme":             "Switch UI theme",
		"health":            "Show runtime health and provider reliability",
		"policy":            "Show circuit breaker and policy state",
		"ledger":            "Inspect side effects ledger of active plan",
		"plan-proof":        "Inspect contract/evidence proof for a plan step",
		"journal":           "Show recent execution journal events",
		"recovery":          "Show latest recovery snapshot",
		"observability":     "Unified reliability and execution dashboard",
		"memory-governance": "Show session archive/retention governance stats",
		"tree-stats":        "Show planner tree depth, branches and limits",
		"agents":            "Show/hide agent orchestrator tree (Ctrl+A)",
		"insights":          "Show learning insights: strategy metrics, patterns, delegation stats",
	}

	if hint, ok := hints[cmd]; ok {
		return hint
	}

	// Partial match for autocomplete
	for fullCmd, hint := range hints {
		if strings.HasPrefix(fullCmd, cmd) {
			return hint
		}
	}

	return ""
}

// prettyPath returns path with the user's home directory collapsed to "~".
// Unconditional — unlike shortenPath this does *not* depend on length, so
// it's safe for the status bar where we always want `~/github/gokin` over
// the longer `/Users/ginkida/github/gokin`. Returns "." for empty input
// so callers don't have to guard.
func prettyPath(path string) string {
	if path == "" {
		return "."
	}
	home, _ := os.UserHomeDir()
	if home == "" || !strings.HasPrefix(path, home) {
		return path
	}
	// Must match either exactly (path == home) or have a separator after
	// so /Users/ginkidaX isn't rewritten as ~X.
	rest := path[len(home):]
	if rest == "" {
		return "~"
	}
	if rest[0] == '/' {
		return "~" + rest
	}
	return path
}

// shortenPath shortens a path to fit within maxLen runes while preserving the filename.
// Uses smart truncation: shows directory prefix + ... + filename.
// Unicode-safe: uses rune count instead of byte length.
func shortenPath(path string, maxLen int) string {
	if utf8.RuneCountInString(path) <= maxLen {
		return path
	}

	// Try to show ~/... for home directory paths
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		path = "~" + path[len(home):]
	}

	runes := []rune(path)
	if len(runes) <= maxLen {
		return path
	}

	// Guard tiny widths: the "..."+tail truncations below compute
	// runes[len(runes)-maxLen+3:], whose start index exceeds len(runes) when
	// maxLen < 3 → a slice-bounds panic that crashes the whole TUI (Bubble Tea's
	// render loop has no recover). Narrow terminals + file autocomplete pass
	// maxLen 1–2 (input.go). Return a bounded tail instead of "...".
	if maxLen < 3 {
		if maxLen <= 0 {
			return ""
		}
		return string(runes[len(runes)-maxLen:])
	}

	// Smart truncation: preserve filename and show as much context as possible
	lastSlash := strings.LastIndex(path, "/")
	if lastSlash == -1 {
		// No slash, truncate from the left
		return "..." + string(runes[len(runes)-maxLen+3:])
	}

	filename := path[lastSlash:] // includes leading /
	filenameRunes := []rune(filename)

	// If filename alone fits, show directory prefix + ... + filename
	if len(filenameRunes) < maxLen-6 { // 6 = len("...") + some prefix
		availableForDir := maxLen - len(filenameRunes) - 3
		if availableForDir > 3 {
			dirRunes := []rune(path)
			return string(dirRunes[:availableForDir]) + "..." + filename
		}
	}

	// Fallback: truncate from the left to always show filename
	return "..." + string(runes[len(runes)-maxLen+3:])
}

// extractToolInfo extracts displayable info from tool arguments.
// This is used as a fallback when the switch statement doesn't match.
func extractToolInfo(args map[string]any) string {
	if args == nil {
		return ""
	}

	// Priority keys - check in order of preference
	priorityKeys := []string{
		"file_path",
		"path",
		"directory_path",
		"source",
		"destination",
		"command",
		"pattern",
		"query",
		"url",
		"action",
		"operation",
		"name",
	}

	for _, key := range priorityKeys {
		if val, ok := args[key]; ok {
			switch v := val.(type) {
			case string:
				if v != "" {
					return shortenPath(v, 50)
				}
			}
		}
	}
	return ""
}

// renderResponseMetadata renders the response metadata as a compact dim footer.
// Format: 1.2k in · 3.4k out · 4.1s
func (m Model) renderResponseMetadata(meta ResponseMetadataMsg) string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var parts []string

	// Input tokens
	if meta.InputTokens > 0 {
		var s string
		if meta.InputTokens >= 1000 {
			s = fmt.Sprintf("%.1fk in", float64(meta.InputTokens)/1000)
		} else {
			s = fmt.Sprintf("%d in", meta.InputTokens)
		}
		parts = append(parts, s)
	}

	// Output tokens
	if meta.OutputTokens > 0 {
		var s string
		if meta.OutputTokens >= 1000 {
			s = fmt.Sprintf("%.1fk out", float64(meta.OutputTokens)/1000)
		} else {
			s = fmt.Sprintf("%d out", meta.OutputTokens)
		}
		parts = append(parts, s)
	}

	// Cache read tokens (prompt caching)
	if meta.CacheReadInputTokens > 0 {
		var s string
		if meta.CacheReadInputTokens >= 1000 {
			s = fmt.Sprintf("%.1fk cached", float64(meta.CacheReadInputTokens)/1000)
		} else {
			s = fmt.Sprintf("%d cached", meta.CacheReadInputTokens)
		}
		parts = append(parts, s)
	}

	// Timing breakdown: total duration with tool/thinking split
	if meta.Duration > 0 {
		toolDur := m.responseToolDuration
		thinkingDur := meta.Duration - toolDur
		if thinkingDur < 0 {
			thinkingDur = 0
		}

		// Show breakdown when tools were used
		if m.responseToolCount > 0 && toolDur > 0 {
			parts = append(parts, fmt.Sprintf("thinking %s", formatCompactDuration(thinkingDur)))
			toolLabel := fmt.Sprintf("tools %s (%s)", formatCompactDuration(toolDur),
				formatToolRunSummary(m.responseToolCount, m.responseToolFailures, false))
			parts = append(parts, toolLabel)
		} else {
			parts = append(parts, formatCompactDuration(meta.Duration))
		}
	}

	// Per-message cost estimate
	if meta.Cost > 0 {
		parts = append(parts, formatResponseCost(meta.Cost))
	}

	// Normally the status bar already shows the selected model, so repeating it
	// in every footer is noise. Surface identity only when an opt-in fallback
	// actually served the response — crucial for attribution and billing.
	if meta.FallbackUsed && meta.Provider != "" {
		identity := meta.Provider
		if meta.Model != "" {
			identity += "/" + meta.Model
		}
		parts = append(parts, "via "+identity)
	}

	if len(parts) == 0 {
		return ""
	}

	// A quiet dim stats line — NO "─ … ─" framing. The dashes were the same
	// inter-turn ruler the v0.100.37 work removed; they re-appeared here on every
	// real turn (tokens are always present). The numbers stay (handy for cost),
	// just without the chrome around them.
	return dimStyle.Render(strings.Join(parts, "  ·  "))
}

func formatResponseCost(cost float64) string {
	if cost <= 0 {
		return ""
	}
	if cost < 0.0001 {
		return "< $0.0001"
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

func formatToolRunSummary(count, failures int, includeUsed bool) string {
	if count <= 0 {
		return ""
	}
	word := "tools"
	if count == 1 {
		word = "tool"
	}
	summary := fmt.Sprintf("%d %s", count, word)
	if includeUsed {
		summary += " used"
	}
	if failures > 0 {
		summary += fmt.Sprintf(" · %d failed", failures)
	}
	return summary
}

// formatCompactDuration formats a duration compactly: "242ms", "1.2s", "2.1m".
func formatCompactDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

// AppendOutput appends text to the output.
func (m *Model) AppendOutput(text string) {
	m.output.AppendText(text)
}

// AppendOutputLine appends a line to the output.
func (m *Model) AppendOutputLine(text string) {
	m.output.AppendLine(text)
}

// AppendMarkdown appends markdown to the output.
func (m *Model) AppendMarkdown(text string) {
	m.output.AppendMarkdown(text)
}

// LoadInputHistory loads input history from file.
func (m *Model) LoadInputHistory() error {
	return m.input.LoadHistory()
}

// SaveInputHistory saves input history to file.
func (m *Model) SaveInputHistory() error {
	return m.input.SaveHistory()
}

// ClearOutput clears the output viewport.
func (m *Model) ClearOutput() {
	m.output.Clear()
}

// ResetInput clears the input field.
func (m *Model) ResetInput() {
	m.input.Reset()
}

// StreamText sends a stream text message.
func StreamText(text string) tea.Cmd {
	return func() tea.Msg {
		return StreamTextMsg(text)
	}
}

// StreamThinking sends a stream thinking message.
func StreamThinking(text string) tea.Cmd {
	return func() tea.Msg {
		return StreamThinkingMsg(text)
	}
}

// ToolCall sends a tool call message.
func ToolCall(name string, args map[string]any) tea.Cmd {
	return func() tea.Msg {
		return ToolCallMsg{Name: name, Args: args}
	}
}

// ToolResult sends a tool result message.
func ToolResult(name string, result string) tea.Cmd {
	return func() tea.Msg {
		return ToolResultMsg{Name: name, Content: result}
	}
}

// ToolProgress sends a tool progress message (heartbeat for long operations).
func ToolProgress(name string, elapsed time.Duration) tea.Cmd {
	return func() tea.Msg {
		return ToolProgressMsg{Name: name, Elapsed: elapsed}
	}
}

// ResponseDone signals that a response is complete.
func ResponseDone() tea.Cmd {
	return func() tea.Msg {
		return ResponseDoneMsg{}
	}
}

// SendError sends an error message.
func SendError(err error) tea.Cmd {
	return func() tea.Msg {
		return ErrorMsg(err)
	}
}

// UpdateTodos updates the todo list display.
func UpdateTodos(items []string) tea.Cmd {
	return func() tea.Msg {
		return TodoUpdateMsg(items)
	}
}

// PermissionRequest sends a permission request message.
func PermissionRequest(toolName string, args map[string]any, riskLevel, reason string) tea.Cmd {
	return func() tea.Msg {
		return PermissionRequestMsg{
			ToolName:  toolName,
			Args:      args,
			RiskLevel: riskLevel,
			Reason:    reason,
		}
	}
}

// QuestionRequest sends a question request message.
func QuestionRequest(question string, options []string, defaultOpt string) tea.Cmd {
	return func() tea.Msg {
		return QuestionRequestMsg{
			Question: question,
			Options:  options,
			Default:  defaultOpt,
		}
	}
}

// PlanApprovalRequest sends a plan approval request message.
func PlanApprovalRequest(title, description string, steps []PlanStepInfo) tea.Cmd {
	return func() tea.Msg {
		return PlanApprovalRequestMsg{
			Title:       title,
			Description: description,
			Steps:       steps,
		}
	}
}

// ResponseMetadata sends a response metadata message.
func ResponseMetadata(model string, inputTokens, outputTokens int, duration time.Duration, toolsUsed []string) tea.Cmd {
	return func() tea.Msg {
		return ResponseMetadataMsg{
			Model:        model,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			Duration:     duration,
			ToolsUsed:    toolsUsed,
		}
	}
}

// lastStreamHeading returns the most recent markdown heading (outside code
// fences) in the streaming buffer — the SECTION the answer is currently
// writing, which reads as an activity ("Root cause analysis") where the raw
// current-line snippet reads as a fragment. Fence awareness matters: shell
// snippets inside ``` blocks are full of `# comment` lines that would
// otherwise masquerade as headings. Returns "" when no heading has streamed.
func lastStreamHeading(buf string) string {
	if buf == "" {
		return ""
	}
	inFence := false
	last := ""
	for _, raw := range strings.Split(buf, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if h := markdownHeadingText(line); h != "" {
			last = h
		}
	}
	return last
}

// markdownHeadingText extracts the text of an ATX heading line ("## Title",
// optionally with closing hashes "## Title ##"). Returns "" for non-headings.
func markdownHeadingText(line string) string {
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(line) || line[i] != ' ' {
		return ""
	}
	text := strings.TrimSpace(line[i+1:])
	// Strip an ATX closing sequence (trailing run of #'s preceded by a space).
	if j := strings.LastIndexByte(text, ' '); j >= 0 && strings.Trim(text[j+1:], "#") == "" {
		text = strings.TrimSpace(text[:j])
	}
	return text
}

// lastStreamSnippet returns the last non-empty line of the streaming buffer for
// the live "Writing: …" preview, keeping its START — the action/intent the model
// is expressing ("Я добавлю несуществующие тесты…"), which is what answers "what
// is it doing now". It does NOT front-truncate (keep the tail): that cut the
// leading verb mid-word ("…лю несуществующие тесты и исправлю типы"), so the line
// read as gibberish. Length is budgeted to the card width so the readable start
// AND the trailing "· <elapsed>" both survive the card's own width clip; trimmed
// at a word boundary with a trailing "…". A short line is returned as-is.
func (m Model) lastStreamSnippet() string {
	buf := m.currentResponseBuf.String()
	if buf == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(buf, "\n"), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" && len(lines) > 1 {
		last = strings.TrimSpace(lines[len(lines)-2])
	}
	if last == "" {
		return ""
	}
	// Reserve ~20 cols for the "Writing: " prefix + " · <elapsed>" suffix so the
	// card's truncateRunes(width-2) clip doesn't eat the elapsed. Floor so a
	// tiny/zero width (before the first WindowSizeMsg) still shows something.
	budget := max(m.width-20, 24)
	runes := []rune(last)
	if len(runes) <= budget {
		return last
	}
	cut := string(runes[:budget])
	if sp := strings.LastIndex(cut, " "); sp > budget/2 {
		cut = cut[:sp] // prefer a word boundary over a mid-word cut
	}
	return strings.TrimRight(cut, " ,.;:—-") + "…"
}
