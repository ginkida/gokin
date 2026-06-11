package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// typeRunes feeds each rune through Model.Update as a key event, returning the
// updated model (Bubble Tea passes the model by value through Update).
func typeRunes(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	for _, r := range s {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		mm := updated.(Model)
		m = &mm
	}
	return m
}

func newTypeAheadModel(t *testing.T) *Model {
	t.Helper()
	m := NewModel()
	// Lay out the model so input/view rendering have a width.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mm := updated.(Model)
	return &mm
}

// TestTypeAhead_TypingDuringProcessingReachesInput pins the core of #8: while
// the agent is processing, keystrokes land in the input box instead of being
// swallowed (pre-change, input.Update only ran in StateInput).
func TestTypeAhead_TypingDuringProcessingReachesInput(t *testing.T) {
	for _, state := range []State{StateProcessing, StateStreaming} {
		m := newTypeAheadModel(t)
		m.state = state
		m = typeRunes(t, m, "next task")
		if got := m.input.Value(); got != "next task" {
			t.Fatalf("state %v: typed text should reach input, got %q", state, got)
		}
	}
}

// TestTypeAhead_EnterQueuesWithoutStateChange — Enter during processing
// submits the composed message (the app layer queues it), stays in the
// processing state, and resets the input for the next composition.
func TestTypeAhead_EnterQueuesWithoutStateChange(t *testing.T) {
	m := newTypeAheadModel(t)
	m.state = StateProcessing
	m.lastSubmitTime = time.Time{} // outside the debounce window

	var submitted []string
	m.SetCallbacks(func(msg string) { submitted = append(submitted, msg) }, nil)

	m = typeRunes(t, m, "queued message")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := updated.(Model)
	m = &mm

	if len(submitted) != 1 || submitted[0] != "queued message" {
		t.Fatalf("onSubmit calls = %v, want [queued message]", submitted)
	}
	if m.state != StateProcessing {
		t.Fatalf("state changed to %v — type-ahead Enter must not leave processing", m.state)
	}
	if m.input.Value() != "" {
		t.Fatalf("input not reset after type-ahead submit: %q", m.input.Value())
	}
}

// TestTypeAhead_EmptyEnterIsNoop — Enter with nothing composed must not call
// onSubmit (would queue an empty message).
func TestTypeAhead_EmptyEnterIsNoop(t *testing.T) {
	m := newTypeAheadModel(t)
	m.state = StateProcessing
	m.lastSubmitTime = time.Time{}

	calls := 0
	m.SetCallbacks(func(string) { calls++ }, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
	if calls != 0 {
		t.Fatalf("empty Enter during processing called onSubmit %d times", calls)
	}
}

// TestTypeAhead_ModalStateBlocksInput — modal overlays own the keyboard;
// type-ahead must not leak keystrokes into the main input underneath them.
func TestTypeAhead_ModalStateBlocksInput(t *testing.T) {
	m := newTypeAheadModel(t)
	m.state = StateQuestionPrompt
	if m.typeAheadActive() {
		t.Fatal("typeAheadActive must be false in a modal state")
	}
}

// TestTypeAhead_QueuedCountBadge — QueuedCountMsg drives the status-bar badge.
func TestTypeAhead_QueuedCountBadge(t *testing.T) {
	m := newTypeAheadModel(t)
	updated, _ := m.Update(QueuedCountMsg(2))
	mm := updated.(Model)
	if mm.queuedPending != 2 {
		t.Fatalf("queuedPending = %d, want 2", mm.queuedPending)
	}
	segs := mm.compactStatusSegments()
	found := false
	for _, s := range segs {
		if strings.Contains(s, "2") && strings.Contains(s, "📥") {
			found = true
		}
	}
	if !found {
		t.Fatalf("status segments missing queued badge: %v", segs)
	}
	// Draining the queue clears the badge.
	updated, _ = mm.Update(QueuedCountMsg(0))
	mm = updated.(Model)
	if mm.queuedPending != 0 {
		t.Fatalf("queuedPending after drain = %d, want 0", mm.queuedPending)
	}
}
