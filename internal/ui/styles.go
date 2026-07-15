package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Colors for the UI theme — Graphite + Violet palette (locked).
// These are the bootstrap defaults used before ApplyTheme runs and must
// stay in sync with ThemeDark in themes.go (the only shipped theme).
var (
	ColorPrimary   = lipgloss.Color("#9B7BFF") // Violet (mockup #7c4dff lifted)
	ColorSecondary = lipgloss.Color("#5BA8C7") // Calm Cyan
	ColorSuccess   = lipgloss.Color("#5AB97B") // Forest Green
	ColorWarning   = lipgloss.Color("#D4A24A") // Deep Amber
	ColorError     = lipgloss.Color("#D85A4A") // Coral
	ColorMuted     = lipgloss.Color("#9A958C") // Readable Warm Gray
	ColorText      = lipgloss.Color("#E8E4D8") // Warm Off-White
	ColorBg        = lipgloss.Color("#0E1116") // Warm Graphite

	// Extended semantic colors
	ColorBorder    = lipgloss.Color("#2A2C33") // Subtle Border
	ColorHighlight = lipgloss.Color("#C0AEFF") // Lavender
	ColorDim       = lipgloss.Color("#807D75") // Accessible Dim Warm Gray
	ColorAccent    = lipgloss.Color("#9B7BFF") // Unified with Primary (single violet)
	ColorRunning   = lipgloss.Color("#6B8AD4") // Info Blue
	ColorInfo      = lipgloss.Color("#6BAEB5") // Calm Teal

	// Modal semantic colors
	ColorContext  = lipgloss.Color("#9A958C") // = Muted (warm gray)
	ColorQuestion = ColorSecondary            // Cyan
	ColorPlan     = ColorInfo                 // Teal

	// Accent palette (used sparingly for gradients, banners, named glyphs).
	// Re-pinned to harmonise with the locked graphite+violet base.
	ColorGradient1 = lipgloss.Color("#9B7BFF") // Violet (= Primary)
	ColorGradient2 = lipgloss.Color("#6B8AD4") // Indigo-ish (= Running)
	ColorGradient3 = lipgloss.Color("#5BA8C7") // Cyan (= Secondary)
	ColorGolden    = lipgloss.Color("#D4A24A") // Amber (= Warning)
	ColorRose      = lipgloss.Color("#D85A4A") // Coral (= Error)
	ColorMint      = lipgloss.Color("#5AB97B") // Forest (= Success)
	ColorLavender  = lipgloss.Color("#C0AEFF") // Lavender (= Highlight)
	ColorSlate     = lipgloss.Color("#16191F") // Surface (raised card)
)

// MessageIcons provides consistent icons for different message types
var MessageIcons = map[string]string{
	"success": "✓",
	"error":   "✗",
	"warning": "⚠",
	"info":    "ℹ",
	"hint":    "›",
	"loading": "◐",
	"done":    "✓",
	"pending": "○",
	"active":  "●",
	"skip":    "↷",
}

// ToastIcons provides icons for toast notifications by type.
var ToastIcons = map[ToastType]string{
	ToastInfo:    "ℹ",
	ToastSuccess: "✓",
	ToastWarning: "⚠",
	ToastError:   "✗",
}

// ToastColors provides colors for toast notifications by type.
var ToastColors = map[ToastType]lipgloss.Color{
	ToastInfo:    ColorInfo,
	ToastSuccess: ColorSuccess,
	ToastWarning: ColorWarning,
	ToastError:   ColorError,
}

// FilePeekIcons provides consistent icons for file peek actions.
var FilePeekIcons = map[string]string{
	"read":      "▸",
	"reading":   "▸",
	"write":     "✦",
	"created":   "✦",
	"edit":      "△",
	"editing":   "△",
	"modifying": "△",
	"delete":    "✗",
	"deleting":  "✗",
	"default":   "▸",
}

// FilePeekColors provides colors for file peek actions.
var FilePeekColors = map[string]lipgloss.Color{
	"read":      ColorSecondary,
	"reading":   ColorSecondary,
	"write":     ColorSuccess,
	"created":   ColorSuccess,
	"edit":      ColorWarning,
	"editing":   ColorWarning,
	"modifying": ColorWarning,
	"delete":    ColorError,
	"deleting":  ColorError,
}

// StatusIcons provides icons for status indicators.
var StatusIcons = map[string]string{
	"active":   "●",
	"pending":  "○",
	"success":  "✓",
	"error":    "✗",
	"warning":  "⚠",
	"info":     "ℹ",
	"waiting":  "◐",
	"thinking": "◐",
	"typing":   "○",
	"idle":     "●",
}

// StatusColors provides colors for status indicators.
var StatusColors = map[string]lipgloss.Color{
	"active":   ColorRunning,
	"pending":  ColorMuted,
	"success":  ColorSuccess,
	"error":    ColorError,
	"warning":  ColorWarning,
	"info":     ColorInfo,
	"waiting":  ColorWarning,
	"thinking": ColorPrimary,
	"typing":   ColorSuccess,
	"idle":     ColorMuted,
}

// ToolIcons provides minimal Unicode glyphs for tool calls — no emoji, consistent rendering.
var ToolIcons = map[string]string{
	"read":         "▸",
	"write":        "✦",
	"edit":         "△",
	"bash":         "$",
	"glob":         "◇",
	"grep":         "⊙",
	"todo":         "☐",
	"diff":         "±",
	"tree":         "⊞",
	"web_fetch":    "↗",
	"web_search":   "↗",
	"list_files":   "◇",
	"list_dir":     "◇",
	"file_search":  "⊙",
	"code_search":  "⊙",
	"ask_question": "?",
	"git_log":      "⎇",
	"git_diff":     "⎇",
	"git_blame":    "⎇",
	"git_add":      "⎇",
	"git_branch":   "⎇",
	"git_commit":   "⎇",
	"git_pr":       "⎇",
	"git_status":   "⎇",
	"commit":       "⎇",
	"copy":         "✦",
	"delete":       "×",
	"mkdir":        "✦",
	"move":         "△",
	"redo":         "↪",
	"memory":       "◈",
	"memorize":     "◈",
	"refactor":     "△",
	"batch":        "▪",
	"task":         "▪",
	"test":         "▪",
	"build":        "▪",
	"default":      "·",
}

// GetToolIcon returns the icon for a given tool name.
func GetToolIcon(toolName string) string {
	// Normalize tool name (lowercase, underscores)
	normalized := strings.ToLower(strings.ReplaceAll(toolName, "-", "_"))

	if icon, ok := ToolIcons[normalized]; ok {
		return icon
	}
	return ToolIcons["default"]
}

// GetToolIconColor returns the semantic color for a given tool name.
func GetToolIconColor(toolName string) lipgloss.Color {
	// Normalize tool name
	normalized := strings.ToLower(strings.ReplaceAll(toolName, "-", "_"))

	// Tool-specific semantic colors
	colors := map[string]lipgloss.Color{
		"read":        ColorPrimary,   // Purple - file operations
		"write":       ColorSuccess,   // Green - creation
		"edit":        ColorWarning,   // Amber - modification
		"bash":        ColorRunning,   // Blue - execution
		"glob":        ColorSecondary, // Cyan - search
		"grep":        ColorInfo,      // Teal - pattern search
		"todo":        ColorAccent,    // Pink - tasks
		"diff":        ColorPrimary,   // Purple - comparison
		"tree":        ColorSuccess,   // Green - structure
		"web_fetch":   ColorGradient2, // Indigo - network
		"web_search":  ColorGradient2, // Indigo - network
		"git_log":     ColorWarning,   // Amber - history
		"git_diff":    ColorPrimary,   // Purple - changes
		"git_blame":   ColorSecondary, // Cyan - attribution
		"git_add":     ColorSuccess,   // Green - staging
		"git_branch":  ColorWarning,   // Amber - branch operations
		"git_commit":  ColorSuccess,   // Green - save
		"git_status":  ColorInfo,      // Teal - repository state
		"git_pr":      ColorGradient2, // Indigo - remote collaboration
		"commit":      ColorSuccess,   // Green - save
		"copy":        ColorSuccess,   // Green - creation
		"delete":      ColorError,     // Red - removal
		"mkdir":       ColorSuccess,   // Green - creation
		"move":        ColorWarning,   // Amber - relocation
		"memory":      ColorGradient1, // Purple - storage
		"refactor":    ColorWarning,   // Amber - transformation
		"batch":       ColorAccent,    // Pink - bulk
		"run_tests":   ColorRunning,   // Blue - testing
		"verify_code": ColorInfo,      // Teal - verification
		"test":        ColorRunning,   // Blue - testing
		"build":       ColorWarning,   // Amber - compilation
	}

	if color, ok := colors[normalized]; ok {
		return color
	}
	return ColorMuted // Default gray
}

// Styles contains all UI styles.
type Styles struct {
	App           lipgloss.Style
	Header        lipgloss.Style
	UserPrompt    lipgloss.Style
	AssistantText lipgloss.Style
	ThoughtText   lipgloss.Style
	ToolCall      lipgloss.Style
	ToolResult    lipgloss.Style
	Error         lipgloss.Style
	Warning       lipgloss.Style
	Spinner       lipgloss.Style
	StatusBar     lipgloss.Style
	Input         lipgloss.Style
	Viewport      lipgloss.Style
	TodoItem      lipgloss.Style
	TodoPending   lipgloss.Style
	TodoActive    lipgloss.Style
	TodoDone      lipgloss.Style

	// Box styles for structured output
	InfoBox    lipgloss.Style
	WarningBox lipgloss.Style
	SuccessBox lipgloss.Style
	ErrorBox   lipgloss.Style

	// Additional styles
	Dim       lipgloss.Style
	Highlight lipgloss.Style
	Accent    lipgloss.Style

	// Modal styles (unified across question prompt, plan approval, model selector)
	ModalTitle    lipgloss.Style // Bold title with ColorQuestion/ColorPlan
	ModalSelected lipgloss.Style // Selected option (bold + ColorSecondary)
	ModalNormal   lipgloss.Style // Normal option (ColorMuted)
	ModalMuted    lipgloss.Style // Dimmed text (ColorDim + italic)
	ModalDefault  lipgloss.Style // Default indicator (ColorContext + italic)

	// Markdown styles
	InlineCode lipgloss.Style // Style for inline code blocks (backticks)

	// Code block styles
	CodeBlockBorder   lipgloss.Style // Border for code blocks
	CodeBlockHeader   lipgloss.Style // Header with filename/language
	CodeBlockSelected lipgloss.Style // Selected code block border
	CodeBlockActions  lipgloss.Style // Action hints in header

	// Tool execution block styles
	ToolBlock     lipgloss.Style // Container for entire tool execution
	ToolHeader    lipgloss.Style // Header with icon + name
	ToolContent   lipgloss.Style // Tool output content
	ToolSeparator lipgloss.Style // Separator line between tools
	ToolSpinner   lipgloss.Style // Animated spinner for active tools
	ToolTiming    lipgloss.Style // Timing information

	// Segmented Status Bar styles
	StatusSegment      lipgloss.Style
	StatusSegmentBold  lipgloss.Style
	StatusSeparator    lipgloss.Style
	StatusSectionName  lipgloss.Style
	StatusSectionValue lipgloss.Style

	// Card styles for message grouping
	UserCard      lipgloss.Style
	AssistantCard lipgloss.Style
}

// rootBackgroundStyle keeps the app-owned background active across nested
// Lip Gloss spans. Nested styles end with a full SGR reset, which would
// otherwise expose a light terminal background until the next styled span.
// Deriving the prefix through the active renderer keeps TrueColor, ANSI256,
// ANSI, and no-color output consistent without hard-coding an escape sequence.
func rootBackgroundStyle(renderer *lipgloss.Renderer, background lipgloss.Color) lipgloss.Style {
	base := renderer.NewStyle().Background(background)
	return base.Transform(func(value string) string {
		const probe = "x"
		probeRender := base.Render(probe)
		probeIndex := strings.Index(probeRender, probe)
		if probeIndex <= 0 {
			// ASCII/no-color renderers emit no style prefix.
			return value
		}

		prefix := probeRender[:probeIndex]
		value = strings.ReplaceAll(value, "\x1b[0m", "\x1b[0m"+prefix)
		return strings.ReplaceAll(value, "\x1b[m", "\x1b[m"+prefix)
	})
}

// DefaultStyles returns the default UI styles.
func DefaultStyles() *Styles {
	return &Styles{
		// The shipped palette is explicitly a dark Graphite theme. Owning the
		// frame background keeps its light foregrounds readable even when the
		// terminal's configured default background is light. In no-color mode
		// Lip Gloss omits both foreground and background escapes, so the
		// terminal's own accessible defaults still win.
		App: rootBackgroundStyle(lipgloss.DefaultRenderer(), ColorBg),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			MarginBottom(1),

		UserPrompt: lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Bold(true),

		AssistantText: lipgloss.NewStyle().
			Foreground(ColorText).
			Padding(0, 1),

		ThoughtText: lipgloss.NewStyle().
			Foreground(ColorDim).
			Italic(true),

		ToolCall: lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Bold(true).
			Background(ColorSlate).
			Padding(0, 1),

		ToolResult: lipgloss.NewStyle().
			Foreground(ColorMuted).
			MarginLeft(2),

		Error: lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true),

		Warning: lipgloss.NewStyle().
			Foreground(ColorWarning).
			Bold(true),

		Spinner: lipgloss.NewStyle().
			Foreground(ColorPrimary),

		StatusBar: lipgloss.NewStyle().
			Foreground(ColorMuted).
			Background(ColorBg).
			Padding(0, 1),

		Input: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			// Quiet neutral frame (not the loud violet brand accent) so the input
			// box doesn't dominate every idle frame — the model's prose, and the
			// violet › prompt, are the focus (CC keeps the input border subtle).
			BorderForeground(ColorDim).
			Padding(0, 1),

		Viewport: lipgloss.NewStyle().
			MarginBottom(1),

		TodoItem: lipgloss.NewStyle().
			MarginLeft(2),

		TodoPending: lipgloss.NewStyle().
			Foreground(ColorMuted),

		TodoActive: lipgloss.NewStyle().
			Foreground(ColorWarning).
			Bold(true),

		TodoDone: lipgloss.NewStyle().
			Foreground(ColorSuccess),

		// Box styles for structured output. ErrorBox has a rounded red
		// border so errors visually separate from streaming output — users
		// previously missed actionable suggestions that blended into text.
		InfoBox:    lipgloss.NewStyle().MarginTop(1),
		WarningBox: lipgloss.NewStyle().MarginTop(1),
		SuccessBox: lipgloss.NewStyle().MarginTop(1),
		ErrorBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorError).
			Padding(0, 1).
			MarginTop(1),

		// Additional styles
		Dim: lipgloss.NewStyle().
			Foreground(ColorDim),

		Highlight: lipgloss.NewStyle().
			Foreground(ColorHighlight),

		Accent: lipgloss.NewStyle().
			Foreground(ColorAccent),

		// Modal styles (unified across prompts)
		ModalTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorSecondary).
			Padding(0, 1),

		ModalSelected: lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorSecondary),

		ModalNormal: lipgloss.NewStyle().
			Foreground(ColorMuted),

		ModalMuted: lipgloss.NewStyle().
			Foreground(ColorDim).
			Italic(true),

		ModalDefault: lipgloss.NewStyle().
			Foreground(ColorContext).
			Italic(true),

		// Markdown styles
		//
		// InlineCode uses ColorBorder (not ColorSlate=surface) as the
		// background — surface (#16191F) is only ~8/256 brighter than
		// the chat background (#0E1116), too subtle to register as an
		// "inline code" affordance on bright monitors. Border (#2A2C33)
		// gives ~28/256 of contrast — readable in any lighting.
		// No horizontal Padding — it inserted a literal space INSIDE the chip on
		// each side, so `foo()` rendered with a double-space gap (` foo() `) that
		// made code-dense prose look stuttery. The Lavender-on-Border contrast
		// already marks the span; CC keeps inline code tight to its words.
		InlineCode: lipgloss.NewStyle().
			Foreground(ColorLavender).
			Background(ColorBorder),

		// Code block styles
		CodeBlockBorder: lipgloss.NewStyle().
			Foreground(ColorBorder),

		CodeBlockHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorAccent),

		CodeBlockSelected: lipgloss.NewStyle().
			Foreground(ColorSecondary).
			Bold(true),

		CodeBlockActions: lipgloss.NewStyle().
			Foreground(ColorInfo),

		// Tool execution block styles
		ToolBlock: lipgloss.NewStyle().
			MarginTop(1).
			MarginBottom(1).
			Padding(0, 1),

		ToolHeader: lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorPrimary).
			Background(ColorSlate).
			Padding(0, 1).
			MarginBottom(1),

		ToolContent: lipgloss.NewStyle().
			Foreground(ColorText).
			PaddingLeft(2),

		ToolSeparator: lipgloss.NewStyle().
			Foreground(ColorDim).
			MarginTop(1).
			MarginBottom(1),

		ToolSpinner: lipgloss.NewStyle().
			Foreground(ColorInfo).
			Bold(true),

		ToolTiming: lipgloss.NewStyle().
			Foreground(ColorMuted).
			Italic(true),

		// Segmented Status Bar (Powerline inspired)
		StatusSegment: lipgloss.NewStyle().
			Background(ColorBg).
			Padding(0, 1),

		StatusSegmentBold: lipgloss.NewStyle().
			Background(ColorBg).
			Bold(true).
			Padding(0, 1),

		StatusSeparator: lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorBorder),

		StatusSectionName: lipgloss.NewStyle().
			Foreground(ColorDim).
			Bold(true),

		StatusSectionValue: lipgloss.NewStyle().
			Foreground(ColorText),

		// Card styles
		UserCard: lipgloss.NewStyle().
			PaddingLeft(2).
			MarginTop(1),

		AssistantCard: lipgloss.NewStyle().
			PaddingLeft(2).
			MarginTop(1),
	}
}

func (s *Styles) FormatUserMessage(msg string) string {
	msg = safeTerminalDisplayText(msg)
	// Violet › prefix matches the input prompt — same glyph signals
	// "your turn" both in scrollback history and at the active input
	// row. Body in warm off-white for readable contrast.
	promptStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	contentStyle := lipgloss.NewStyle().Foreground(ColorText)

	lines := strings.Split(msg, "\n")
	var result strings.Builder
	for i, line := range lines {
		if i == 0 {
			result.WriteString(promptStyle.Render("› ") + contentStyle.Render(line))
		} else {
			result.WriteString("\n  " + contentStyle.Render(line))
		}
	}
	return s.UserCard.Render(result.String())
}

// FormatAssistantMessage formats an assistant message.
func (s *Styles) FormatAssistantMessage(msg string) string {
	return s.AssistantCard.Render(s.AssistantText.Render(msg))
}

// FormatAssistantStreaming formats streaming assistant text (without header).
// Used during streaming to avoid repeating the header.
func (s *Styles) FormatAssistantStreaming(msg string) string {
	return s.AssistantText.Render(msg)
}

// FormatToolCall formats a tool call notification.
func (s *Styles) FormatToolCall(name string) string {
	return s.ToolCall.Render(safeKeyEntryText(name))
}

// FormatToolCallWithArgs formats a tool call with brief argument summary.
func (s *Styles) FormatToolCallWithArgs(name string, args map[string]any) string {
	name = safeKeyEntryText(name)
	summary := formatArgsSummary(args)
	if summary != "" {
		return s.ToolCall.Render(name + " " + summary)
	}
	return s.ToolCall.Render(name)
}

// FormatToolExecuting formats a tool that is currently executing.
func (s *Styles) FormatToolExecuting(name string, args map[string]any) string {
	name = safeKeyEntryText(name)
	summary := formatArgsSummary(args)

	spinner := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	idx := int(time.Now().Unix()/100) % len(spinner)

	exeStyle := lipgloss.NewStyle().Foreground(ColorDim)

	if summary != "" {
		return exeStyle.Render(spinner[idx] + " " + name + " " + summary)
	}
	return exeStyle.Render(spinner[idx] + " " + name)
}

// toolBullet is the single shared marker for every tool line. It is a SMALL
// SQUARE (U+25AA) — deliberately NOT the filled-record-circle U+23FA, which many
// terminals render as a wide colored emoji. Rendered DIM so it marks the line
// without shouting; tools stay distinguishable via the per-tool name color.
const toolBullet = "▪"

// FormatToolExecutingBlock formats a tool call as a compact line.
// Format: ▪ Read(file.go)  (dim marker, per-tool name color)
func (s *Styles) FormatToolExecutingBlock(name string, args map[string]any) string {
	name = safeKeyEntryText(name)
	// One shared dim marker for every tool — quiet and aligned.
	bulletStyle := lipgloss.NewStyle().Foreground(ColorDim)

	// Tool name — per-tool color (so tools are still distinguishable) but not bold.
	nameStyle := lipgloss.NewStyle().Foreground(GetToolIconColor(name))

	// Args — soft white, not gray (more readable)
	argsStyle := lipgloss.NewStyle().Foreground(ColorText)

	var result strings.Builder
	result.WriteString(bulletStyle.Render(toolBullet + " "))
	result.WriteString(nameStyle.Render(capitalizeToolName(name)))

	argsStr := buildClaudeCodeArgs(name, args)
	if argsStr != "" {
		result.WriteString(" ")
		result.WriteString(argsStyle.Render(argsStr))
	}

	return result.String()
}

// FormatToolSuccess formats a successful tool result.
func (s *Styles) FormatToolSuccess(name string, duration time.Duration) string {
	name = safeKeyEntryText(name)
	successStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	durationStr := ""
	if duration < time.Second {
		durationStr = fmt.Sprintf("%dms", duration.Milliseconds())
	} else if duration < time.Minute {
		durationStr = fmt.Sprintf("%.1fs", duration.Seconds())
	}

	if durationStr != "" {
		return successStyle.Render("✓ "+name) + "  " + dimStyle.Render(durationStr)
	}
	return successStyle.Render("✓ " + name)
}

// minDisplayedToolDuration is the floor below which a tool's timing is omitted
// from its success line. Sub-100ms operations are effectively instant; a
// trailing "9ms" on every read/grep is noise during exploration. Slow tools
// (≥100ms) still show timing — that's the part worth noticing.
const minDisplayedToolDuration = 100 * time.Millisecond

// formatToolDurationLabel renders a tool's wall-clock as a short label + the
// color it should use. Returns "" below minDisplayedToolDuration (sub-100ms ops
// are effectively instant — a trailing "9ms" on every read is noise).
func formatToolDurationLabel(duration time.Duration) (string, lipgloss.Color) {
	if duration < minDisplayedToolDuration {
		return "", ColorDim
	}
	switch {
	case duration < time.Second:
		return fmt.Sprintf("%dms", duration.Milliseconds()), ColorDim
	case duration < time.Minute:
		secs := duration.Seconds()
		color := ColorDim
		if secs > 5 {
			color = ColorWarning
		}
		return fmt.Sprintf("%.1fs", secs), color
	default:
		return fmt.Sprintf("%.1fm", duration.Minutes()), ColorWarning
	}
}

// FormatToolLine renders a completed tool as ONE compact line:
//
//	▪ Read(credentials.go) · 175 lines · 340ms
//
// The marker is dim, the name keeps its per-tool color, the target sits in
// parens, the outcome + duration are dim. Nothing is repeated — the call subject
// and the result outcome share a single row (the older two-line ✓-block repeated
// the tool name AND the full path/command on the result row).
func (s *Styles) FormatToolLine(name, target, outcome string, duration time.Duration) string {
	name = safeKeyEntryText(name)
	target = strings.TrimSpace(safeInlineDisplayText(target))
	outcome = strings.TrimSpace(safeInlineDisplayText(outcome))
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	nameStyle := lipgloss.NewStyle().Foreground(GetToolIconColor(name))
	targetStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(markerStyle.Render(toolBullet + " "))
	b.WriteString(nameStyle.Render(capitalizeToolName(name)))
	if target != "" {
		b.WriteString(targetStyle.Render("(" + target + ")"))
	}
	if outcome != "" {
		b.WriteString(dimStyle.Render(" · " + outcome))
	}
	if durStr, durColor := formatToolDurationLabel(duration); durStr != "" {
		b.WriteString(dimStyle.Render(" · "))
		b.WriteString(lipgloss.NewStyle().Foreground(durColor).Render(durStr))
	}
	return b.String()
}

// FormatToolFailureLine renders a FAILED tool as a flat one-liner matching the
// success line's shape — `▪ ✗ Name(target) · duration` (red ✗). Pass and fail
// now read as the same calm shape; the error detail nests under ⎿ below (the way
// Claude Code shows `⎿ Error: …`), instead of the old rounded red alarm box.
func (s *Styles) FormatToolFailureLine(name, target string, duration time.Duration) string {
	name = safeKeyEntryText(name)
	target = strings.TrimSpace(safeInlineDisplayText(target))
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	errStyle := lipgloss.NewStyle().Foreground(ColorError)
	nameStyle := lipgloss.NewStyle().Foreground(GetToolIconColor(name))
	targetStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var b strings.Builder
	b.WriteString(markerStyle.Render(toolBullet + " "))
	b.WriteString(errStyle.Render("✗ "))
	b.WriteString(nameStyle.Render(capitalizeToolName(name)))
	if target != "" {
		b.WriteString(targetStyle.Render("(" + target + ")"))
	}
	if durStr, durColor := formatToolDurationLabel(duration); durStr != "" {
		b.WriteString(dimStyle.Render(" · "))
		b.WriteString(lipgloss.NewStyle().Foreground(durColor).Render(durStr))
	}
	return b.String()
}

// FormatToolError formats a failed tool result.
func (s *Styles) FormatToolError(name string, err error) string {
	name = safeKeyEntryText(name)
	errorStyle := lipgloss.NewStyle().Foreground(ColorRose)
	msgStyle := lipgloss.NewStyle().Foreground(ColorDim)

	errText := "unknown error"
	if err != nil {
		errText = safeKeyEntryText(err.Error())
	}
	return errorStyle.Render("✗ "+name) + "  " + msgStyle.Render(errText)
}

// --- Agent Activity Formatting ---
// Completed sub-agent tool calls shown with a dim type prefix, one calm line
// each carrying the OUTCOME (matches the foreground tool line):
//   explore ▪ Read(app.go) · 175 lines · 1.2s
//   general ▪ ✗ Bash(go build ./...) · exit code 1
//   explore ✓ 5.2s
// Flat format because background agents are async — their events
// interleave with other output, making tree connectors unreliable.

// FormatAgentToolLine renders a COMPLETED sub-agent tool call as one calm line
// carrying its outcome — `  <type> ▪ Name(target) · outcome` (red ✗ + summary on
// failure). Mirrors the foreground FormatToolLine shape (v0.100.37) with a dim
// agent-type prefix, so sub-agent work reads the same as foreground work and
// shows MEANINGFUL output instead of a bare tool name. The running tool is shown
// live by the status bar / activity card, so there is no separate start row.
//
// No per-tool duration: a sub-agent runs read-only tools CONCURRENTLY
// (executeToolsParallel), and the only per-agent timestamp available to the UI
// (SubAgentState.LastToolTime) is clobbered by sibling start/end events, so a
// duration computed here would be mis-attributed. Accurate timing would have to
// be measured at the source (agent.executeTool) and threaded through — not worth
// a 7-param callback for a polish label. The agent's TOTAL elapsed still shows
// on completion (FormatAgentComplete).
func (s *Styles) FormatAgentToolLine(agentType, name, target, summary string, success bool) string {
	agentType = safeKeyEntryText(agentType)
	name = safeKeyEntryText(name)
	target = strings.TrimSpace(safeInlineDisplayText(target))
	summary = strings.TrimSpace(safeInlineDisplayText(summary))
	prefixStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	nameStyle := lipgloss.NewStyle().Foreground(GetToolIconColor(name))
	targetStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	errStyle := lipgloss.NewStyle().Foreground(ColorError)

	var b strings.Builder
	b.WriteString(prefixStyle.Render("  " + agentType + " "))
	b.WriteString(markerStyle.Render(toolBullet + " "))
	if !success {
		b.WriteString(errStyle.Render("✗ "))
	}
	b.WriteString(nameStyle.Render(capitalizeToolName(name)))
	if target != "" {
		b.WriteString(targetStyle.Render("(" + target + ")"))
	}
	if summary != "" {
		b.WriteString(dimStyle.Render(" · "))
		if success {
			b.WriteString(dimStyle.Render(summary))
		} else {
			b.WriteString(errStyle.Render(summary))
		}
	}
	return b.String()
}

// FormatAgentComplete renders agent completion.
func (s *Styles) FormatAgentComplete(agentType string, elapsed time.Duration) string {
	agentType = safeKeyEntryText(agentType)
	prefixStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	checkStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	durStyle := lipgloss.NewStyle().Foreground(ColorDim)

	dur := formatCompactDuration(elapsed)
	return prefixStyle.Render("  "+agentType+" ") + checkStyle.Render("✓") + " " + durStyle.Render(dur)
}

// FormatAgentFailed renders agent failure.
func (s *Styles) FormatAgentFailed(agentType string, elapsed time.Duration) string {
	agentType = safeKeyEntryText(agentType)
	prefixStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	crossStyle := lipgloss.NewStyle().Foreground(ColorError)
	durStyle := lipgloss.NewStyle().Foreground(ColorDim)

	dur := formatCompactDuration(elapsed)
	return prefixStyle.Render("  "+agentType+" ") + crossStyle.Render("✗") + " " + durStyle.Render(dur)
}

// formatArgsSummary creates a brief summary of tool arguments.
func formatArgsSummary(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}

	// Priority keys to show first
	priorityKeys := []string{"path", "file_path", "command", "pattern", "content", "query"}

	var parts []string
	shown := make(map[string]bool)

	// Show priority keys first
	for _, key := range priorityKeys {
		if val, ok := args[key]; ok {
			str := formatArgValue(val)
			if str != "" {
				parts = append(parts, key+"="+str)
				shown[key] = true
				if len(parts) >= 2 {
					break
				}
			}
		}
	}

	// Add other keys if we have room
	for key, val := range args {
		if shown[key] {
			continue
		}
		if len(parts) >= 2 {
			break
		}
		str := formatArgValue(val)
		if str != "" {
			parts = append(parts, safeKeyEntryText(key)+"="+str)
		}
	}

	if len(parts) == 0 {
		return ""
	}

	result := "(" + lipgloss.NewStyle().Foreground(ColorMuted).Render(
		joinStrings(parts, ", "),
	) + ")"
	return result
}

// formatArgValue formats a single argument value for display.
func formatArgValue(val any) string {
	switch v := val.(type) {
	case string:
		// Don't truncate here - let the caller decide based on context
		// This allows file paths to be shown in full
		return "\"" + safeInlineDisplayText(v) + "\""
	case float64:
		return fmt.Sprintf("%.0f", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

// joinStrings joins strings with a separator.
func joinStrings(parts []string, sep string) string {
	return strings.Join(parts, sep)
}

// FormatError formats an error message with proper styling.
func (s *Styles) FormatError(err string) string {
	return s.FormatErrorWithSuggestion(err, "", "")
}

// FormatErrorWithSuggestion formats an error message with a suggestion - Claude Code style.
func (s *Styles) FormatErrorWithSuggestion(err, suggestion, code string) string {
	err = safeKeyEntryText(err)
	suggestion = safeKeyEntryText(suggestion)
	code = safeKeyEntryText(code)
	errorStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(ColorText)
	suggestionStyle := lipgloss.NewStyle().Foreground(ColorWarning)
	codeStyle := lipgloss.NewStyle().Foreground(ColorDim)
	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var result strings.Builder
	result.WriteString(errorStyle.Render("✗ Error: ") + msgStyle.Render(err))

	if suggestion != "" {
		result.WriteString("\n" + markerStyle.Render("  ⎿  ") + suggestionStyle.Render(suggestion))
	}
	if code != "" {
		result.WriteString("\n" + markerStyle.Render("     ") + codeStyle.Render("("+code+")"))
	}
	return result.String()
}

// capitalizeToolName converts snake_case tool names to PascalCase.
// e.g., "web_fetch" → "WebFetch", "bash" → "Bash", "read" → "Read"
func capitalizeToolName(name string) string {
	name = safeKeyEntryText(name)
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

// buildClaudeCodeArgs formats tool arguments for display in Claude Code style.
// Returns a string like "file_path=/path" or "command" for bash.
func buildClaudeCodeArgs(name string, args map[string]any) string {
	name = safeKeyEntryText(name)
	if len(args) == 0 {
		return ""
	}

	var result string
	isFilePath := false

	switch name {
	case "bash":
		if cmd, ok := args["command"]; ok {
			result = formatArgValue(cmd)
		}
	case "read", "write", "edit":
		if path, ok := args["file_path"]; ok {
			result = formatArgValue(path)
			isFilePath = true
		}
	case "copy", "move":
		result = formatPathPair(toolStringArg(args, "source"), toolStringArg(args, "destination"), 70)
	case "delete", "mkdir":
		if path := toolStringArg(args, "path"); path != "" {
			result = path
			isFilePath = true
		}
	case "run_tests":
		result = formatRunTestsTarget(args, 45)
	case "verify_code":
		path := toolStringArg(args, "path")
		if path == "" {
			path = "."
		}
		result = shortenPath(path, 55)
	case "git_status":
		path := toolStringArg(args, "path")
		if path != "" {
			result = shortenPath(path, 45)
		}
		if toolBoolArg(args, "short") {
			if result != "" {
				result += " "
			}
			result += "--short"
		}
	case "git_diff":
		result = formatGitDiffTarget(args, 55)
	case "git_add":
		result = formatGitAddTarget(args, 55)
	case "git_branch":
		result = strings.TrimSpace(toolStringArg(args, "action") + " " + toolStringArg(args, "name"))
	case "git_log":
		if file := toolStringArg(args, "file"); file != "" {
			result = shortenPath(file, 45)
		} else if grep := toolStringArg(args, "grep"); grep != "" {
			result = "grep=" + compactInline(grep, 36)
		} else if author := toolStringArg(args, "author"); author != "" {
			result = "author=" + compactInline(author, 36)
		} else if count := toolIntArg(args, "count"); count > 0 {
			result = fmt.Sprintf("%d commits", count)
		}
	case "grep":
		if pattern, ok := args["pattern"]; ok {
			result = "pattern=" + formatArgValue(pattern)
		}
		if path, ok := args["path"]; ok {
			result += " path=" + formatArgValue(path)
		}
	case "glob":
		if pattern, ok := args["pattern"]; ok {
			result = "pattern=" + formatArgValue(pattern)
		}
	case "web_fetch":
		if url, ok := args["url"]; ok {
			result = "url=" + formatArgValue(url)
		}
	case "web_search":
		if query, ok := args["query"]; ok {
			result = "query=" + formatArgValue(query)
		}
	default:
		// Use first priority arg
		for _, key := range []string{"file_path", "path", "directory_path", "source", "destination", "command", "pattern", "query", "url", "action", "operation", "name"} {
			if val, ok := args[key]; ok {
				result = key + "=" + formatArgValue(val)
				if key == "file_path" || key == "path" || key == "directory_path" || key == "source" || key == "destination" {
					isFilePath = true
				}
				break
			}
		}
	}

	// Smart truncation. File paths get an aggressive budget + ~/ prefix
	// for home directory + middle-ellipsis (shortenPath), because the
	// success line right below this one repeats the path — long absolute
	// paths like /Users/alice/github/project/internal/ui/tui.go produced
	// two ugly duplicated rows. 55 cols keeps the filename visible even
	// in narrow terminals while fitting the exec header on one line.
	const filePathMaxLen = 55
	const generalArgMaxLen = 120
	if isFilePath {
		if stripped := stripWrappingQuotes(result); stripped != result {
			// shortenPath expects a bare path, not a quoted one. Re-wrap.
			result = "\"" + shortenPath(stripped, filePathMaxLen) + "\""
		} else {
			result = shortenPath(result, filePathMaxLen)
		}
	} else {
		result = compactInline(result, generalArgMaxLen)
	}

	return result
}

// stripWrappingQuotes returns s without its single wrapping pair of double
// quotes, or s unchanged. Used to unwrap formatArgValue's quoted strings
// before path-aware truncation; callers re-wrap.
func stripWrappingQuotes(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// RenderToolSeparator renders a visual separator between tool executions.
func (s *Styles) RenderToolSeparator() string {
	separator := strings.Repeat("─", 80)
	return s.ToolSeparator.Render(separator)
}

// RenderMessageSeparator renders a subtle separator between messages.
func (s *Styles) RenderMessageSeparator() string {
	separatorStyle := lipgloss.NewStyle().
		Foreground(ColorDim)
	return separatorStyle.Render(strings.Repeat("─", 60))
}

// FormatThinkingIndicator formats the thinking/processing indicator with braille spinner.
func (s *Styles) FormatThinkingIndicator() string {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	idx := int(time.Now().UnixMilli()/100) % len(spinners)

	spinnerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	textStyle := lipgloss.NewStyle().Foreground(ColorDim)

	return spinnerStyle.Render(spinners[idx]) + textStyle.Render(" Thinking")
}

// FormatThinkingIndicatorWithTokens formats the thinking indicator with token usage.
func (s *Styles) FormatThinkingIndicatorWithTokens(usedTokens, maxTokens int) string {
	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	idx := int(time.Now().UnixMilli()/100) % len(spinners)

	spinnerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	textStyle := lipgloss.NewStyle().Foreground(ColorDim)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	// Format token count
	var tokenStr string
	if usedTokens >= 1000 {
		tokenStr = fmt.Sprintf("%.1fk", float64(usedTokens)/1000)
	} else {
		tokenStr = fmt.Sprintf("%d", usedTokens)
	}
	var maxStr string
	if maxTokens >= 1000 {
		maxStr = fmt.Sprintf("%.0fk", float64(maxTokens)/1000)
	} else {
		maxStr = fmt.Sprintf("%d", maxTokens)
	}

	return spinnerStyle.Render(spinners[idx]) + textStyle.Render(" Thinking") +
		dimStyle.Render("  ["+tokenStr+"/"+maxStr+"]")
}

// FormatPlanStepHeader formats a plan step header for delegated execution output.
func (s *Styles) FormatPlanStepHeader(stepID, totalSteps int, title string) string {
	title = safeKeyEntryText(title)
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorPrimary)
	progressStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	borderStyle := lipgloss.NewStyle().
		Foreground(ColorDim)

	progress := progressStyle.Render(fmt.Sprintf("[%d/%d]", stepID, totalSteps))
	header := headerStyle.Render(title)
	line := borderStyle.Render(strings.Repeat("─", 60))

	return line + "\n" + progress + " " + header + "\n"
}

// FormatPlanStepResult formats a plan step completion result.
func (s *Styles) FormatPlanStepResult(stepID int, success bool, summary string) string {
	summary = strings.TrimSpace(safeInlineDisplayText(summary))
	summary = compactInline(summary, 120)
	if success {
		checkStyle := lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
		summaryStyle := lipgloss.NewStyle().Foreground(ColorText)
		result := checkStyle.Render(fmt.Sprintf("  Step %d done", stepID))
		if summary != "" {
			result += " " + summaryStyle.Render(summary)
		}
		return result
	}

	failStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	msgStyle := lipgloss.NewStyle().Foreground(ColorText)
	result := failStyle.Render(fmt.Sprintf("  Step %d failed", stepID))
	if summary != "" {
		result += " " + msgStyle.Render(summary)
	}
	return result
}

// FormatPlanBanner formats the plan execution start/end banner.
func (s *Styles) FormatPlanBanner(text string, isStart bool) string {
	text = safeKeyEntryText(text)
	color := ColorSuccess
	if isStart {
		color = ColorInfo
	}
	borderStyle := lipgloss.NewStyle().Foreground(color)
	textStyle := lipgloss.NewStyle().Foreground(color).Bold(true)

	line := strings.Repeat("─", 60)
	return borderStyle.Render(line) + "\n" + textStyle.Render(text) + "\n" + borderStyle.Render(line)
}

// FormatMessage formats a message with consistent styling based on type.
// msgType can be: success, error, warning, info, hint, loading
func (s *Styles) FormatMessage(msgType, title, body string) string {
	msgType = safeKeyEntryText(msgType)
	title = safeKeyEntryText(title)
	body = safeTerminalDisplayText(body)
	icon := MessageIcons[msgType]
	if icon == "" {
		icon = MessageIcons["info"]
	}

	var color lipgloss.Color

	switch msgType {
	case "success", "done":
		color = ColorSuccess
	case "error":
		color = ColorError
	case "warning":
		color = ColorWarning
	case "hint":
		color = ColorAccent
	default: // info, loading
		color = ColorInfo
	}

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(color)

	bodyStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		PaddingLeft(2)

	header := headerStyle.Render(icon + " " + title)
	if body == "" {
		return header
	}

	return header + "\n" + bodyStyle.Render(body)
}

// FormatHint formats a contextual hint message.
func (s *Styles) FormatHint(text string) string {
	text = safeKeyEntryText(text)
	hintStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Italic(true)
	iconStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)

	return iconStyle.Render("› ") + hintStyle.Render(text)
}
