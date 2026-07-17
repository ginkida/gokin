package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// End-of-turn recap (the Claude-Code "※ recap: … Next: …" idiom, v0.100.91).
// After a substantive tool-using turn, one dim line anchors where the work
// stands — how many checklist items are done and what comes next — so a user
// returning to the session doesn't have to reconstruct state from scrollback.
//
// Calm-UI constraints (all load-bearing):
//   - Only when the todos panel is HIDDEN — with the panel open the same
//     information is already on screen (the hidden-panel-reminder pattern,
//     same rule as the status bar's "Ctrl+T tasks N" hint).
//   - Only after a turn that actually ran tools; a conversational turn gets
//     no recap.
//   - Only for lists of 2+ items — a single-item list is covered by the
//     answer itself.
//   - Deduplicated: if nothing changed since the previous recap, stay silent
//     instead of stamping an identical line under every turn.

// buildTurnRecap composes the recap line from the live todo checklist the
// app already streams to the UI (emitTodoUpdate's glyph-prefixed rows).
// Returns "" when no recap should be shown. Mutates m.lastRecapLine (the
// dedup latch) only when a line IS emitted.
func (m *Model) buildTurnRecap() string {
	if m.todosVisible || len(m.todoItems) == 0 || m.responseToolCount == 0 {
		return ""
	}

	done, total := 0, 0
	inProgress, firstPending := "", ""
	for _, item := range m.todoItems {
		switch {
		case strings.HasPrefix(item, "● "):
			done++
			total++
		case strings.HasPrefix(item, "◐ "):
			if inProgress == "" {
				inProgress = strings.TrimPrefix(item, "◐ ")
			}
			total++
		case strings.HasPrefix(item, "○ "):
			if firstPending == "" {
				firstPending = strings.TrimPrefix(item, "○ ")
			}
			total++
		}
	}
	if total < 2 {
		return ""
	}

	next := inProgress
	if next == "" {
		next = firstPending
	}
	var text string
	switch {
	case next != "":
		text = fmt.Sprintf("※ %d/%d tasks · next: %s  (ctrl+t)",
			done, total, truncateForWidth(next, 64))
	case done == total:
		text = fmt.Sprintf("※ all %d tasks done", total)
	default:
		return ""
	}

	if text == m.lastRecapLine {
		return ""
	}
	m.lastRecapLine = text
	return lipgloss.NewStyle().Foreground(ColorDim).Render(text)
}
