package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// failureBody renders a failed tool's detail as red lines (capped, with a
// "⋯ N more" marker) plus the /undo hint when the tool is undoable. The caller
// nests this under the ⎿ corner below the flat failure line — same emitter the
// success path uses, so pass/fail share one calm shape (no rounded red box).
func failureBody(detail, toolName string) string {
	detail = safeTerminalDisplayText(detail)
	toolName = safeKeyEntryText(toolName)
	const maxLines = 4
	errStyle := lipgloss.NewStyle().Foreground(ColorError)
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	var lines []string
	for raw := range strings.SplitSeq(strings.TrimSpace(detail), "\n") {
		raw = strings.TrimRight(raw, " \t")
		if strings.TrimSpace(raw) == "" {
			continue
		}
		lines = append(lines, raw)
	}

	hidden := 0
	markerIndex := -1
	if len(lines) > maxLines {
		hidden = len(lines) - maxLines
		// Preserve both the cause at the head and recovery context at the tail.
		// Provider/tool errors commonly put the concrete next step after a stack
		// trace; keeping only the first four rows silently amputated that action.
		const headLines = maxLines / 2
		tailLines := maxLines - headLines
		visible := make([]string, 0, maxLines)
		visible = append(visible, lines[:headLines]...)
		visible = append(visible, lines[len(lines)-tailLines:]...)
		lines = visible
		markerIndex = headLines
	}

	var out []string
	for index, l := range lines {
		if index == markerIndex {
			word := "lines"
			if hidden == 1 {
				word = "line"
			}
			out = append(out, moreStyle.Render(fmt.Sprintf("⋯ %d more %s", hidden, word)))
		}
		out = append(out, errStyle.Render(l))
	}
	if hint := renderErrorActionHints(toolName); hint != "" {
		out = append(out, hint)
	}
	return strings.Join(out, "\n")
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
