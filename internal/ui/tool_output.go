package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ToolOutputConfig configures the tool output display behavior.
type ToolOutputConfig struct {
	MaxCollapsedLines int     // Maximum lines to show when collapsed (default: 10)
	MaxCollapsedChars int     // Maximum characters when collapsed (default: 500)
	HeadRatio         float64 // Ratio of lines from start (default: 0.66)
	ShowLineNumbers   bool    // Whether to show line numbers in expanded view
	ExpandHint        string  // Text to show for expand hint
	CollapseHint      string  // Text to show for collapse hint
}

// DefaultToolOutputConfig returns the default configuration.
func DefaultToolOutputConfig() ToolOutputConfig {
	return ToolOutputConfig{
		MaxCollapsedLines: 10,
		MaxCollapsedChars: 500,
		HeadRatio:         0.66,
		ShowLineNumbers:   true,
		ExpandHint:        "e",
		CollapseHint:      "e",
	}
}

// ToolOutputEntry represents a single tool output entry.
type ToolOutputEntry struct {
	ToolName    string
	FullContent string
	Expanded    bool
	Index       int
}

// ToolOutputModel manages the display of tool output with expand/collapse functionality.
type ToolOutputModel struct {
	entries      []ToolOutputEntry
	config       ToolOutputConfig
	styles       *Styles
	AllExpanded  bool
	AllCollapsed bool
}

// NewToolOutputModel creates a new tool output model.
func NewToolOutputModel(styles *Styles) *ToolOutputModel {
	return &ToolOutputModel{
		entries: make([]ToolOutputEntry, 0),
		config:  DefaultToolOutputConfig(),
		styles:  styles,
	}
}

// SetConfig updates the configuration.
func (m *ToolOutputModel) SetConfig(config ToolOutputConfig) {
	m.config = config
}

// AddEntry adds a new tool output entry.
func (m *ToolOutputModel) AddEntry(toolName, content string) int {
	entry := ToolOutputEntry{
		ToolName:    toolName,
		FullContent: content,
		Expanded:    m.AllExpanded,
		Index:       len(m.entries),
	}
	m.entries = append(m.entries, entry)
	return entry.Index
}

// ToggleExpand toggles the expand state of an entry.
func (m *ToolOutputModel) ToggleExpand(index int) bool {
	if index < 0 || index >= len(m.entries) {
		return false
	}
	m.entries[index].Expanded = !m.entries[index].Expanded
	return m.entries[index].Expanded
}

// IsExpanded returns whether an entry is expanded.
func (m *ToolOutputModel) IsExpanded(index int) bool {
	if index < 0 || index >= len(m.entries) {
		return false
	}
	return m.entries[index].Expanded
}

// GetLatestIndex returns the index of the most recent entry.
func (m *ToolOutputModel) GetLatestIndex() int {
	if len(m.entries) == 0 {
		return -1
	}
	return len(m.entries) - 1
}

// GetEntry returns the entry at the given index safely.
// Returns nil if the index is out of bounds.
func (m *ToolOutputModel) GetEntry(index int) *ToolOutputEntry {
	if index < 0 || index >= len(m.entries) {
		return nil
	}
	return &m.entries[index]
}

// ToggleLatest toggles the expand state of the most recent entry.
func (m *ToolOutputModel) ToggleLatest() bool {
	return m.ToggleExpand(m.GetLatestIndex())
}

// ToggleAll toggles all entries between expanded and collapsed.
func (m *ToolOutputModel) ToggleAll() {
	if m.AllExpanded {
		m.AllExpanded = false
		m.AllCollapsed = true
	} else {
		m.AllExpanded = true
		m.AllCollapsed = false
	}
	for i := range m.entries {
		m.entries[i].Expanded = m.AllExpanded
	}
}

// CompactModeActive returns true when future long tool outputs should stay compact.
func (m *ToolOutputModel) CompactModeActive() bool {
	return m.AllCollapsed
}

// GetSummary returns a compact summary for the entry at the given index.
func (m *ToolOutputModel) GetSummary(index int) string {
	if index < 0 || index >= len(m.entries) {
		return ""
	}
	entry := m.entries[index]
	lineCount := strings.Count(entry.FullContent, "\n") + 1
	info := entry.ToolName
	if entry.FullContent != "" {
		info += fmt.Sprintf(": %d lines", lineCount)
	}
	return fmt.Sprintf("[%s]", info)
}

// NeedsTruncation checks if the content needs truncation.
func (m *ToolOutputModel) NeedsTruncation(content string) bool {
	if len(content) > m.config.MaxCollapsedChars {
		return true
	}
	lines := strings.Split(content, "\n")
	return len(lines) > m.config.MaxCollapsedLines
}

// RenderEntry renders a tool output entry.
func (m *ToolOutputModel) RenderEntry(index int) string {
	if index < 0 || index >= len(m.entries) {
		return ""
	}

	entry := m.entries[index]
	content := entry.FullContent

	if content == "" {
		return ""
	}

	if entry.Expanded || !m.NeedsTruncation(content) {
		// Show full content
		return m.renderFull(content, entry.Expanded && m.NeedsTruncation(content))
	}

	// Show truncated content
	return m.renderTruncated(content)
}

// RenderContent renders content directly without storing it.
func (m *ToolOutputModel) RenderContent(content string, expanded bool) string {
	if content == "" {
		return ""
	}

	if expanded || !m.NeedsTruncation(content) {
		return m.renderFull(content, expanded && m.NeedsTruncation(content))
	}

	return m.renderTruncated(content)
}

// renderFull renders the full content, optionally with line numbers.
func (m *ToolOutputModel) renderFull(content string, _ bool) string {
	var result strings.Builder

	if m.config.ShowLineNumbers {
		lines := strings.Split(content, "\n")
		lineNumWidth := len(fmt.Sprintf("%d", len(lines)))
		lineNumStyle := lipgloss.NewStyle().Foreground(ColorDim)

		for i, line := range lines {
			lineNum := fmt.Sprintf("%*d", lineNumWidth, i+1)
			result.WriteString(lineNumStyle.Render(lineNum) + " │ " + line)
			if i < len(lines)-1 {
				result.WriteString("\n")
			}
		}
	} else {
		result.WriteString(content)
	}

	// No collapse hint needed

	return result.String()
}

// renderHiddenLinesHint formats the "X lines hidden" indicator shown between
// the head and tail preview of a truncated tool output.
//
// Earlier versions rendered a cryptic "... +95 lines ..." which looked like
// a placeholder rather than an action-hinting UI element. The new shape:
//
//	        ▼ 95 more lines · press e to expand
//
// reads as a sentence, tells the user there's content below, and spells
// out the interaction. Dim-coloured so it doesn't compete visually with
// the actual code lines around it.
func renderHiddenLinesHint(hidden int) string {
	if hidden <= 0 {
		return ""
	}
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	word := "lines"
	if hidden == 1 {
		word = "line"
	}
	return dimStyle.Render(fmt.Sprintf("    ▼ %d more %s · press e to expand", hidden, word))
}

// renderTruncated renders truncated content with head and tail.
//
// Picks the head lines by skipping leading noise — for code files, the
// first lines are `package X`, blank separators, and imports, none of
// which tell you what's actually in the file. We skip past those until we
// hit a line that likely carries signal (a top-level declaration, a log
// message, a command prompt, …) and show from there. Tail lines get the
// symmetric treatment: trailing blank lines and lone `}` from the closing
// brace are skipped.
//
// Heuristic is intentionally cheap and language-agnostic: line-based
// predicates (`isNoiseHead` / `isNoiseTail`). If nothing "interesting"
// is found, we fall back to the original first-N-and-last-M behaviour so
// the user always sees *something*.
func (m *ToolOutputModel) renderTruncated(content string) string {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Calculate how many lines from head and tail
	headCount := int(float64(m.config.MaxCollapsedLines) * m.config.HeadRatio)
	tailCount := m.config.MaxCollapsedLines - headCount

	if headCount < 1 {
		headCount = 1
	}
	if tailCount < 1 {
		tailCount = 1
	}

	headStart := firstSignalLine(lines)
	// Don't skip so far that we'd overlap with the tail section — leave
	// room for at least the configured number of head lines.
	if headStart > totalLines-headCount {
		headStart = 0
	}

	tailStop := lastSignalLine(lines)
	// Similarly clamp on the tail side.
	if tailStop < headStart+headCount {
		tailStop = totalLines - 1
	}

	var result strings.Builder

	// Head lines
	headEnd := headStart + headCount
	if headEnd > totalLines {
		headEnd = totalLines
	}
	for i := headStart; i < headEnd; i++ {
		line := lines[i]
		if runes := []rune(line); len(runes) > 100 {
			line = string(runes[:97]) + "..."
		}
		result.WriteString(line)
		if i < headEnd-1 || tailCount > 0 {
			result.WriteString("\n")
		}
	}

	// Hidden lines indicator — accounts for skipped prefix/suffix plus the
	// gap between head and tail, so the "+N" count matches what's actually
	// missing from view.
	startTail := tailStop - tailCount + 1
	if startTail < headEnd {
		startTail = headEnd
	}
	if startTail > totalLines {
		startTail = totalLines
	}
	hiddenLines := (startTail - headEnd) + headStart + (totalLines - 1 - tailStop)
	if hiddenLines > 0 {
		result.WriteString("\n" + renderHiddenLinesHint(hiddenLines) + "\n")
	}

	// Tail lines
	for i := startTail; i <= tailStop && i < totalLines; i++ {
		line := lines[i]
		if runes := []rune(line); len(runes) > 100 {
			line = string(runes[:97]) + "..."
		}
		result.WriteString(line)
		if i < totalLines-1 && i < tailStop {
			result.WriteString("\n")
		}
	}

	return result.String()
}

// firstSignalLine returns the index of the first line that carries real
// content signal — skipping leading blanks, `package X`, and contiguous
// `import`/`use`/`from` blocks. Returns 0 when there's nothing to skip
// or the content doesn't look like code. The returned index is always a
// valid index into `lines` (or 0 if the slice is empty).
func firstSignalLine(lines []string) int {
	i := 0
	// Skip leading blanks.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	// Skip a `package …` declaration (Go/Dart/Kotlin/Swift).
	if i < len(lines) && strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "package ") {
		i++
	}
	// Skip blanks between package and imports.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	// Skip an import block — either Go-style `import (` block or a run of
	// `import …` / `use …` / `from … import …` single-line statements
	// (covers Python, JS/TS, Rust, Java, C/C++ `#include`).
	switch {
	case i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "import ("):
		// Multi-line Go-style import block: skip until closing ')'.
		i++
		for i < len(lines) {
			s := strings.TrimSpace(lines[i])
			i++
			if s == ")" {
				break
			}
		}
	default:
		for i < len(lines) {
			s := strings.TrimLeft(lines[i], " \t")
			if strings.HasPrefix(s, "import ") ||
				strings.HasPrefix(s, "from ") ||
				strings.HasPrefix(s, "use ") ||
				strings.HasPrefix(s, "#include ") ||
				strings.HasPrefix(s, "require ") ||
				strings.TrimSpace(s) == "" {
				i++
				continue
			}
			break
		}
	}
	// Skip blank lines after the (possibly multi-line) import block so
	// the preview starts on the first real declaration, not the empty
	// separator between imports and code.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	// Don't overshoot the buffer.
	if i >= len(lines) {
		return 0
	}
	return i
}

// lastSignalLine returns the index of the last "interesting" line —
// skipping trailing blanks and lone closing braces (`}`, `})`, `)`).
// Falls back to the last index when nothing would remain.
func lastSignalLine(lines []string) int {
	i := len(lines) - 1
	for i > 0 {
		s := strings.TrimSpace(lines[i])
		if s == "" || s == "}" || s == "})" || s == ")" || s == "});" || s == "};" {
			i--
			continue
		}
		break
	}
	return i
}

// ExecutionStatusRenderer renders tool execution status with enhanced user feedback
type ExecutionStatusRenderer struct {
	styles *Styles
}

// NewExecutionStatusRenderer creates a new execution status renderer
func NewExecutionStatusRenderer(styles *Styles) *ExecutionStatusRenderer {
	return &ExecutionStatusRenderer{styles: styles}
}

// RenderValidation renders safety validation results
func (r *ExecutionStatusRenderer) RenderValidation(toolName string, warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}

	var result strings.Builder

	headerStyle := lipgloss.NewStyle().
		Foreground(ColorWarning).
		Bold(true)

	result.WriteString(headerStyle.Render(fmt.Sprintf("⚠ Safety Warnings for %s:", toolName)))
	result.WriteString("\n")

	for _, warning := range warnings {
		warningStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		result.WriteString(warningStyle.Render("  • " + warning))
		result.WriteString("\n")
	}

	return result.String()
}

// RenderStart renders the start of tool execution with user-friendly summary
func (r *ExecutionStatusRenderer) RenderStart(toolName string, summary interface{}) string {
	var result strings.Builder

	// Tool name with icon
	iconStyle := lipgloss.NewStyle().Foreground(ColorInfo)
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)

	var icon string
	switch toolName {
	case "read":
		icon = ToolIcons["read"]
	case "write":
		icon = ToolIcons["write"]
	case "edit":
		icon = ToolIcons["edit"]
	case "bash":
		icon = ToolIcons["bash"]
	case "grep":
		icon = ToolIcons["grep"]
	case "glob":
		icon = ToolIcons["glob"]
	case "diff", "git_diff":
		icon = ToolIcons["diff"]
	case "git_log", "git_blame":
		icon = ToolIcons["git_log"]
	case "git_status", "git_add", "git_commit", "git_branch", "git_pr":
		icon = ToolIcons["git_status"]
	case "web_fetch", "web_search":
		icon = ToolIcons["web_fetch"]
	case "batch":
		icon = ToolIcons["batch"]
	case "ask_agent", "coordinate":
		icon = MessageIcons["hint"]
	case "task", "run_tests":
		icon = ToolIcons["test"]
	case "delete":
		icon = MessageIcons["error"]
	case "copy", "move":
		icon = ToolIcons["default"]
	case "mkdir":
		icon = ToolIcons["list_dir"]
	case "memory", "memorize", "history_search":
		icon = ToolIcons["memory"]
	case "enter_plan_mode", "update_plan_progress", "get_plan_status", "exit_plan_mode":
		icon = MessageIcons["info"]
	case "ssh":
		icon = MessageIcons["warning"]
	case "list_dir", "tree":
		icon = ToolIcons["tree"]
	default:
		icon = ToolIcons["default"]
	}

	result.WriteString(iconStyle.Render(icon))
	result.WriteString(" ")
	result.WriteString(nameStyle.Render(fmt.Sprintf("%s", toolName)))

	// Add summary if available
	if s, ok := summary.(string); ok && s != "" {
		summaryStyle := lipgloss.NewStyle().Faint(true)
		result.WriteString(" ")
		result.WriteString(summaryStyle.Render("─ " + s))
	}

	result.WriteString("\n")

	return result.String()
}

// RenderProgress renders progress update for long-running operations
func (r *ExecutionStatusRenderer) RenderProgress(toolName string, elapsed time.Duration) string {
	progressStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	return progressStyle.Render(fmt.Sprintf("  ⏳ %s running... %v", toolName, elapsed.Round(time.Second)))
}

// RenderSuccess renders successful tool execution
func (r *ExecutionStatusRenderer) RenderSuccess(toolName string, duration time.Duration) string {
	successStyle := lipgloss.NewStyle().
		Foreground(ColorSuccess).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var result strings.Builder
	result.WriteString(successStyle.Render("  ✓ " + toolName))
	result.WriteString(" ")
	result.WriteString(dimStyle.Render(fmt.Sprintf("(%v)", duration.Round(time.Millisecond))))
	result.WriteString("\n")

	return result.String()
}

// RenderError renders failed tool execution
func (r *ExecutionStatusRenderer) RenderError(toolName string, errMsg string, duration ...time.Duration) string {
	errorStyle := lipgloss.NewStyle().
		Foreground(ColorError).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var result strings.Builder
	result.WriteString(errorStyle.Render("  ✗ " + toolName))
	if len(duration) > 0 && duration[0] > 0 {
		result.WriteString(" ")
		result.WriteString(dimStyle.Render(fmt.Sprintf("(%v)", duration[0].Round(time.Millisecond))))
	}
	result.WriteString("\n")
	result.WriteString(dimStyle.Render("    Error: " + errMsg))
	result.WriteString("\n")

	return result.String()
}

// RenderDenied renders permission denied status
func (r *ExecutionStatusRenderer) RenderDenied(toolName, reason string) string {
	denyStyle := lipgloss.NewStyle().
		Foreground(ColorError).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var result strings.Builder
	result.WriteString(denyStyle.Render("  🚫 " + toolName + " denied"))
	result.WriteString("\n")
	if reason != "" {
		result.WriteString(dimStyle.Render("    Reason: " + reason))
		result.WriteString("\n")
	}

	return result.String()
}

// RenderApproved renders permission approved status
func (r *ExecutionStatusRenderer) RenderApproved(toolName string, summary interface{}) string {
	approveStyle := lipgloss.NewStyle().
		Foreground(ColorSuccess).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var result strings.Builder
	result.WriteString(approveStyle.Render("  ✓ " + toolName + " approved"))

	if s, ok := summary.(string); ok && s != "" {
		result.WriteString(" ")
		result.WriteString(dimStyle.Render("─ " + s))
	}
	result.WriteString("\n")

	return result.String()
}

// Clear clears all entries.
func (m *ToolOutputModel) Clear() {
	m.entries = make([]ToolOutputEntry, 0)
}

// EntryCount returns the number of entries.
func (m *ToolOutputModel) EntryCount() int {
	return len(m.entries)
}

// FormatToolOutput formats tool output with smart truncation.
// This is a convenience function that can be used directly without storing entries.
func FormatToolOutput(content string, maxLines int, expanded bool) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	if expanded || len(lines) <= maxLines {
		return content
	}

	// Smart truncation: 66% head, 33% tail
	headCount := int(float64(maxLines) * 0.66)
	tailCount := maxLines - headCount

	if headCount < 1 {
		headCount = 1
	}
	if tailCount < 1 {
		tailCount = 1
	}

	var result strings.Builder

	// Head
	for i := 0; i < headCount && i < len(lines); i++ {
		result.WriteString(lines[i])
		result.WriteString("\n")
	}

	// Hidden indicator - clean and subtle
	hiddenLines := len(lines) - headCount - tailCount
	if hiddenLines > 0 {
		result.WriteString(renderHiddenLinesHint(hiddenLines))
		result.WriteString("\n")
	}

	// Tail
	startTail := len(lines) - tailCount
	if startTail < headCount {
		startTail = headCount
	}
	for i := startTail; i < len(lines); i++ {
		result.WriteString(lines[i])
		if i < len(lines)-1 {
			result.WriteString("\n")
		}
	}

	return result.String()
}
