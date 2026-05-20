package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestToolResultCardHasNoBorderAtAnyWidth pins the v0.84.3 polish:
// success tool result cards render as plain 4-space-indented lines, no
// rounded border at any terminal width. The border still wraps diff
// approvals (scene C) and error cards (scene D) — those are higher-
// stakes events that benefit from segmentation. Routine tool success
// blends with the rest of the agent's output.
//
// If a future commit reintroduces lipgloss.RoundedBorder() in
// emitToolResultCard, the border runes ╭╮╰╯│ will appear in rendered
// output and this test trips.
func TestToolResultCardHasNoBorderAtAnyWidth(t *testing.T) {
	cases := []struct {
		name  string
		width int
	}{
		{"narrow", 40},
		{"medium", 80},
		{"wide", 140},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.width = tc.width
			m.handleToolResultWithInfo(
				"grep result line 1\ngrep result line 2",
				"grep",
				"pattern",
				time.Now().Add(-100*time.Millisecond),
			)
			rendered := stripAnsi(m.output.state.content.String())
			for _, r := range []string{"╭", "╮", "╰", "╯", "│"} {
				if strings.Contains(rendered, r) {
					t.Fatalf("width=%d: tool result card should not contain border rune %q, got:\n%s",
						tc.width, r, rendered)
				}
			}
		})
	}
}

// TestCtrlEExpandsLastToolOutput pins the Claude Code-style Ctrl+E
// binding for tool output expand/collapse. Plain `e` is preserved as
// a single-key alternative when input is empty; Ctrl+E is the primary
// because modifier combos don't fight the textarea's normal keys
// (you can be typing and still expand).
func TestCtrlEExpandsLastToolOutput(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.handleToolResultWithInfo(
		strings.Repeat("output line\n", 30),
		"bash",
		"ls /tmp",
		time.Now().Add(-50*time.Millisecond),
	)
	if m.lastToolOutputIndex < 0 {
		t.Fatal("setup: handleToolResultWithInfo should record lastToolOutputIndex")
	}
	wasExpanded := m.toolOutput.IsExpanded(m.lastToolOutputIndex)

	// Ctrl+E toggles even with text in the input — modifier combos
	// bypass the empty-input guard that plain `e` requires.
	m.input.textarea.SetValue("typing something")
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlE})

	if got := m.toolOutput.IsExpanded(m.lastToolOutputIndex); got == wasExpanded {
		t.Fatalf("Ctrl+E should toggle expand state (was %v, still %v)", wasExpanded, got)
	}
}

// TestPlainEStillTogglesWhenInputEmpty pins backward compat: users
// with muscle memory for plain `e` still get the toggle when the input
// is empty. Adding Ctrl+E didn't remove plain `e`.
func TestPlainEStillTogglesWhenInputEmpty(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.handleToolResultWithInfo(
		strings.Repeat("output line\n", 30),
		"bash",
		"ls /tmp",
		time.Now().Add(-50*time.Millisecond),
	)
	wasExpanded := m.toolOutput.IsExpanded(m.lastToolOutputIndex)

	// Empty input is the gate for plain `e`. (NewModel() starts empty.)
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if got := m.toolOutput.IsExpanded(m.lastToolOutputIndex); got == wasExpanded {
		t.Fatalf("plain `e` should still toggle when input is empty (was %v, still %v)", wasExpanded, got)
	}
}

// TestPlainEIgnoredWhenInputHasText is the inverse gate: plain `e` is
// a letter the textarea wants to consume when the user is typing.
// Only Ctrl+E works in that state.
func TestPlainEIgnoredWhenInputHasText(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.handleToolResultWithInfo(
		strings.Repeat("output line\n", 30),
		"bash",
		"ls /tmp",
		time.Now().Add(-50*time.Millisecond),
	)
	wasExpanded := m.toolOutput.IsExpanded(m.lastToolOutputIndex)
	m.input.textarea.SetValue("not empty")

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if got := m.toolOutput.IsExpanded(m.lastToolOutputIndex); got != wasExpanded {
		t.Fatalf("plain `e` should be ignored while typing (was %v, now %v)", wasExpanded, got)
	}
}
