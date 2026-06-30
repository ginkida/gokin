package ui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCtrlDScrollsHalfPageDownWhenInputEmpty(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(0)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.output.viewport.YOffset; got != 5 {
		t.Fatalf("YOffset after Ctrl+D = %d, want half page 5", got)
	}
	if !m.output.IsFrozen() {
		t.Fatalf("Ctrl+D away from bottom should keep output frozen")
	}
}

func TestCtrlDUnfreezesAtBottom(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 20 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(8)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if !m.output.IsAtBottom() {
		t.Fatalf("Ctrl+D near bottom should reach bottom")
	}
	if m.output.IsFrozen() {
		t.Fatalf("Ctrl+D at bottom should unfreeze output")
	}
}

// TestPgUpFreezesDuringStreaming: paging up while the agent streams pauses
// auto-follow so the user can read (instead of being snapped back to the bottom).
func TestPgUpFreezesDuringStreaming(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.GotoBottom()
	m.output.SetFrozen(false) // following

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.output.IsAtBottom() {
		t.Fatalf("PgUp should scroll away from the bottom")
	}
	if !m.output.IsFrozen() {
		t.Fatalf("PgUp during streaming must freeze stream auto-follow")
	}
}

// TestPgDownToBottomUnfreezes: paging back to the bottom resumes auto-follow.
func TestPgDownToBottomUnfreezes(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(28) // 2 from the bottom (maxOffset 30)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if !m.output.IsAtBottom() {
		t.Fatalf("PgDown should reach the bottom")
	}
	if m.output.IsFrozen() {
		t.Fatalf("PgDown to the bottom must resume auto-follow (unfreeze)")
	}
}

// TestMouseWheelUpFreezesDuringStreaming: wheel-up while streaming freezes; the
// fix that lets you scroll WHILE the agent writes.
func TestMouseWheelUpFreezesDuringStreaming(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.GotoBottom()
	m.output.SetFrozen(false)

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	mv := updated.(Model)
	if mv.output.IsAtBottom() {
		t.Fatalf("wheel-up should scroll away from the bottom")
	}
	if !mv.output.IsFrozen() {
		t.Fatalf("mouse wheel-up during streaming must freeze auto-follow")
	}
}

// TestSubmitResetsFrozen: sending a new message clears a leftover scroll-up
// freeze so the new response is followed from the bottom.
func TestSubmitResetsFrozen(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.SetFrozen(true) // user had scrolled up during the previous turn
	m.input.textarea.SetValue("next question")

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.output.IsFrozen() {
		t.Fatalf("submitting a new message must reset frozen so the response is followed")
	}
}

// TestResponseStartUnfreezesForDequeuedTurn: a type-ahead/dequeued turn never
// hits the UI KeyEnter reset, so frozen is cleared when its response STARTS
// streaming (first chunk). A subsequent mid-response scroll-up must STICK.
func TestResponseStartUnfreezesForDequeuedTurn(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.SetFrozen(true) // user had scrolled up during the prior turn
	m.responseHeaderShown = false

	updated, _ := m.Update(StreamTextMsg("hello"))
	mv := updated.(Model)
	if mv.output.IsFrozen() {
		t.Fatalf("first chunk of a new response must clear the scroll-up freeze")
	}

	// Mid-response scroll-up freezes again; the rest of THIS response must not
	// snap back (responseHeaderShown is already true → no re-reset).
	mv.output.SetFrozen(true)
	updated2, _ := mv.Update(StreamTextMsg(" world"))
	mv2 := updated2.(Model)
	if !mv2.output.IsFrozen() {
		t.Fatalf("a mid-response scroll-up must stay frozen, not snap back")
	}
}

func TestCtrlDDoesNotScrollWhileTyping(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(0)
	m.input.textarea.SetValue("draft")

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.output.viewport.YOffset; got != 0 {
		t.Fatalf("Ctrl+D while typing should not scroll, YOffset=%d", got)
	}
}
