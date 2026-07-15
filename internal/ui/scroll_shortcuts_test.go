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

func TestCtrlDDoesNotSnapBackNearBottom(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := range 41 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	maxYOffset := max(m.output.viewport.TotalLineCount()-m.output.viewport.Height, 0)
	step := m.output.viewport.Height / 2
	// Land exactly one line short of the true bottom after Ctrl+D. The former
	// <=2 proximity rule unfroze here even though AtBottom was false.
	m.output.viewport.SetYOffset(maxYOffset - 1 - step)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	wantOffset := maxYOffset - 1
	if got := m.output.viewport.YOffset; got != wantOffset || m.output.IsAtBottom() {
		t.Fatalf("Ctrl+D setup/result offset=%d bottom=%v, want offset %d above bottom", got, m.output.IsAtBottom(), wantOffset)
	}
	if !m.output.IsFrozen() {
		t.Fatal("Ctrl+D one line short of bottom must keep stream follow frozen")
	}

	// Prove the user retains viewport ownership when more output arrives.
	m.output.AppendLine("new streamed line")
	if got := m.output.viewport.YOffset; got != wantOffset || !m.output.IsFrozen() {
		t.Fatalf("next stream line snapped viewport: offset=%d frozen=%v, want %d/true", got, m.output.IsFrozen(), wantOffset)
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

// TestCtrlFDoesNotSnapBackNearBottom pins the fix for a regression the
// v0.100.59 wheel/PgUp/PgDn fix introduced elsewhere: ctrl+f used a
// `total-bottom<=2` PROXIMITY heuristic to decide when to unfreeze, instead
// of the exact IsAtBottom() check. That heuristic could unfreeze while the
// viewport was still 1-2 lines short of the true bottom (not at the bottom
// per AtBottom()), so the next streamed chunk would then snap the viewport
// the rest of the way with no further user input — the same class of bug
// the wheel/PgUp/PgDn fix was built to eliminate, just left behind here.
func TestCtrlFDoesNotSnapBackNearBottom(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := range 41 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	maxYOffset := max(m.output.viewport.TotalLineCount()-m.output.viewport.Height, 0)
	// Land 1 line short of the true bottom after the +3 step — within the
	// old "<=2" proximity band that wrongly unfroze, but NOT AtBottom().
	m.output.viewport.SetYOffset(maxYOffset - 1 - 3)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlF})

	if m.output.viewport.YOffset != maxYOffset-1 {
		t.Fatalf("YOffset after ctrl+f = %d, want %d", m.output.viewport.YOffset, maxYOffset-1)
	}
	if m.output.IsAtBottom() {
		t.Fatalf("test setup invalid: expected NOT at bottom after this step")
	}
	if !m.output.IsFrozen() {
		t.Fatalf("ctrl+f one line short of the true bottom must stay frozen (no snap-back)")
	}
}

// TestCtrlFUnfreezesAtTrueBottom: ctrl+f that actually reaches the bottom
// still resumes auto-follow (the fix must not regress the true-bottom case).
func TestCtrlFUnfreezesAtTrueBottom(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	maxYOffset := max(m.output.viewport.TotalLineCount()-m.output.viewport.Height, 0)
	m.output.viewport.SetYOffset(maxYOffset - 3) // ctrl+f's +3 step lands exactly at the bottom.
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlF})

	if !m.output.IsAtBottom() {
		t.Fatalf("test setup invalid: expected to reach the true bottom")
	}
	if m.output.IsFrozen() {
		t.Fatalf("ctrl+f reaching the true bottom must unfreeze auto-follow")
	}
}

// TestSubmitResetsFrozen: sending a new message clears a leftover scroll-up
// freeze so the new response is followed from the bottom.
func TestSubmitResetsFrozen(t *testing.T) {
	m := NewModel()
	m.SetCallbacks(func(string) {}, nil)
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
