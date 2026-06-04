package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// errorCardMinWidth is the lower bound below which tool-failure rendering
// drops back to the legacy single-line FormatToolError emission. The
// rounded card chrome plus the action-hint row burn too many cells below
// this width for the structure to read.
const errorCardMinWidth = 60

// renderToolErrorCard returns a multi-line inline error card for a failed
// tool result (mockup scene D — "error is a card, not a panic"). Returns
// "" when the terminal is too narrow; the caller should fall back to the
// legacy unwrapped emission in that case.
//
// detail is the primary failure text (errText or first non-empty line of
// the tool's stdout/stderr). Long detail is line-split and capped to keep
// the card from becoming a full-screen log dump — the agent's follow-up
// message typically explains, this card just frames the signal.
func renderToolErrorCard(width int, toolName string, duration time.Duration, detail string) string {
	if width < errorCardMinWidth {
		return ""
	}

	if toolName == "" {
		toolName = "tool"
	}

	innerWidth := width - 4 // 2 border cells + 2 padding cells

	// Title row: ✗ <tool> · <duration>
	crossStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	toolStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	metaStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	sepStyle := lipgloss.NewStyle().Foreground(ColorDim)

	title := crossStyle.Render("✗") + " " + toolStyle.Render(capitalizeToolName(toolName))
	if duration > 0 {
		title += sepStyle.Render(" · ") + metaStyle.Render(formatCompactDuration(duration))
	}

	// Body — split detail into lines, render up to 4 with mild styling.
	body := renderErrorBody(detail, innerWidth)

	parts := []string{title}
	if body != "" {
		parts = append(parts, "", body)
	}
	// Only append a hint row when there's a genuinely-actionable hint — a
	// failed read/grep/bash has none, so the card stays just title + detail.
	if hint := renderErrorActionHints(toolName); hint != "" {
		parts = append(parts, "", hint)
	}
	inner := strings.Join(parts, "\n")

	cardStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorError).
		Padding(0, 1).
		Width(innerWidth)
	return cardStyle.Render(inner)
}

// renderErrorBody renders up to errorCardMaxBodyLines from detail with
// muted styling and a "+N more" indicator when truncated. Long single
// lines are passed through verbatim — lipgloss's Width on the outer card
// hard-wraps them.
func renderErrorBody(detail string, _ int) string {
	const maxLines = 4
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}

	lineStyle := lipgloss.NewStyle().Foreground(ColorText)
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	var nonEmpty []string
	for raw := range strings.SplitSeq(detail, "\n") {
		raw = strings.TrimRight(raw, " \t")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, raw)
	}

	if len(nonEmpty) == 0 {
		return ""
	}

	shown := nonEmpty
	hidden := 0
	if len(nonEmpty) > maxLines {
		shown = nonEmpty[:maxLines]
		hidden = len(nonEmpty) - maxLines
	}

	var b strings.Builder
	for i, line := range shown {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(lineStyle.Render(line))
	}
	if hidden > 0 {
		b.WriteString("\n")
		b.WriteString(moreStyle.Render(fmt.Sprintf("⋯ %d more line", hidden)))
		if hidden > 1 {
			b.WriteString(moreStyle.Render("s"))
		}
	}
	return b.String()
}

// renderErrorActionHints returns the suggested-next-actions row shown at
// the bottom of the error card. Mockup scene D shape — three concrete
// suggestions the user can pick at a keystroke.
//
// Visual rhythm mirrors renderDiffActionHints: primary recovery (retry)
// pops in ColorSuccess bold, abort (esc cancel) pops in ColorError bold,
// alternatives sit in muted middle ground. Keeps both card surfaces on
// the same design language so users don't context-switch between them.
// renderErrorActionHints returns the bottom hint row for the tool-error card,
// or "" when there's no honest, tool-relevant action to offer.
//
// A failed tool is scrollback the agent recovers from on its own — there is NO
// per-card "press Enter to retry" keystroke (no StateToolError), so we don't
// pretend there is, and Esc is the always-available global interrupt rather
// than a card action. The one genuinely useful, tool-specific recovery is
// rolling back the last file mutation after a failed edit/write/etc., so that
// is the only hint we surface, and only for tools the undo manager tracks.
func renderErrorActionHints(toolName string) string {
	if !toolIsUndoable(toolName) {
		return ""
	}
	cmdStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	verbStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	return cmdStyle.Render("/undo") + " " + verbStyle.Render("revert last change")
}

// toolIsUndoable reports whether a failed tool left a file mutation the undo
// manager can revert. Read-only / command tools (bash, read, grep, …) leave
// nothing to undo.
func toolIsUndoable(toolName string) bool {
	switch strings.ToLower(strings.ReplaceAll(toolName, "-", "_")) {
	case "edit", "write", "delete", "move", "copy", "refactor", "mkdir", "batch":
		return true
	}
	return false
}
