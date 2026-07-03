package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A bare-printable shortcut key (plain 'e' toggling a tool card) consumed in
// StateInput must NOT also be typed into the textarea. This goes through the
// FULL Update path — the pre-fix bug was that handleKeyMsg returned nil after
// consuming the key, so Update fell through to the type-ahead input.Update
// forward and the char leaked (card toggled AND input.Value()=="e"). Existing
// tests call handleGlobalKeys directly and miss the double-forward.
func TestUpdate_PlainEShortcutDoesNotLeakIntoInput(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput
	m.handleToolResultWithInfo(
		strings.Repeat("output line\n", 30),
		"bash", "ls /tmp",
		time.Now().Add(-50*time.Millisecond),
	)
	if m.lastToolOutputIndex < 0 {
		t.Fatal("setup: expected a tool card")
	}
	wasExpanded := m.toolOutput.IsExpanded(m.lastToolOutputIndex)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m2 := updated.(Model)

	if got := m2.toolOutput.IsExpanded(m2.lastToolOutputIndex); got == wasExpanded {
		t.Fatalf("plain 'e' should toggle the card (was %v, still %v)", wasExpanded, got)
	}
	if v := m2.input.Value(); v != "" {
		t.Fatalf("plain 'e' shortcut must NOT also type into the input; got %q", v)
	}
}

// The inverse must still hold: with no tool card to toggle, plain 'e' is a
// normal character and must type into the input (starting a message with "e").
func TestUpdate_PlainEWithNoCardTypesNormally(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m2 := updated.(Model)

	if v := m2.input.Value(); v != "e" {
		t.Fatalf("with no card, plain 'e' must type normally; got %q", v)
	}
}

// Ctrl+E with no expandable tool output must FALL THROUGH to the bubbles
// textarea's native binding (ctrl+e = jump to line end), NOT be consumed.
// Regression (v0.100.62 review catch): the tool-toggle handler returned the
// keyConsumed sentinel for Ctrl+E even when there was nothing to toggle, which
// short-circuited Update before it forwarded the key to the textarea — silently
// killing the emacs-style LineEnd binding (sibling of the still-working Ctrl+A =
// LineStart). Observed through the full Update path: type text, move the cursor
// off the end, press Ctrl+E, then type — the char must land at the END.
func TestUpdate_CtrlEWithNoCardMovesCursorToLineEnd(t *testing.T) {
	base := NewModel()
	base.width = 80
	base.state = StateInput

	updated, _ := base.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m := updated.(Model)
	if v := m.input.Value(); v != "hello" {
		t.Fatalf("setup: input = %q, want %q", v, "hello")
	}

	// Move the cursor two columns left → "hel|lo" (Left is a textarea binding,
	// not intercepted by handleGlobalKeys).
	for i := 0; i < 2; i++ {
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m = updated.(Model)
	}

	// Ctrl+E must jump the cursor back to line end.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	m = updated.(Model)

	// Type 'X'. Cursor at end → "helloX"; if Ctrl+E was swallowed the cursor is
	// still mid-line → "helXlo".
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	m = updated.(Model)

	if v := m.input.Value(); v != "helloX" {
		t.Fatalf("Ctrl+E with no card must forward to the textarea LineEnd binding; got %q, want %q ( %q means Ctrl+E was swallowed)", v, "helloX", "helXlo")
	}
}

// Shift+E (toggle-all) is the same class — consumed when there are cards, must
// not leak 'E' into the input.
func TestUpdate_ShiftEToggleAllDoesNotLeakIntoInput(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput
	m.handleToolResultWithInfo(
		strings.Repeat("output line\n", 30),
		"bash", "ls /tmp",
		time.Now().Add(-50*time.Millisecond),
	)
	if m.toolOutput == nil || m.toolOutput.EntryCount() == 0 {
		t.Fatal("setup: expected a tool card")
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'E'}})
	m2 := updated.(Model)

	if v := m2.input.Value(); v != "" {
		t.Fatalf("Shift+E toggle-all must NOT also type into the input; got %q", v)
	}
}
