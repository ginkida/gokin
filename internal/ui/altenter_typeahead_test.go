package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Alt+Enter is advertised as "insert newline". During type-ahead the same key
// must not submit a half-written queued message just because the model is
// currently Processing/Streaming.
func TestAltEnterDuringTypeAheadInsertsNewline(t *testing.T) {
	for _, state := range []State{StateProcessing, StateStreaming} {
		m := newTypeAheadModel(t)
		m.state = state

		var submitted []string
		m.SetCallbacks(func(s string) { submitted = append(submitted, s) }, nil)

		m = typeRunes(t, m, "refactor the parser but")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
		mm := updated.(Model)
		m = &mm

		if len(submitted) != 0 {
			t.Fatalf("state %v: Alt+Enter submitted %q; want newline insertion only", state, submitted)
		}
		if got, want := m.input.textarea.Value(), "refactor the parser but\n"; got != want {
			t.Fatalf("state %v: raw input = %q, want %q", state, got, want)
		}
		if m.state != state {
			t.Fatalf("state %v: Alt+Enter changed state to %v", state, m.state)
		}
	}
}

func TestAltEnterInStateInputInsertsNewline(t *testing.T) {
	m := newTypeAheadModel(t)
	m.state = StateInput

	var submitted []string
	m.SetCallbacks(func(s string) { submitted = append(submitted, s) }, nil)

	m = typeRunes(t, m, "hello")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	mm := updated.(Model)
	m = &mm

	if len(submitted) != 0 {
		t.Fatalf("StateInput Alt+Enter must not submit, got %q", submitted)
	}
	if got, want := m.input.textarea.Value(), "hello\n"; got != want {
		t.Fatalf("StateInput Alt+Enter raw input = %q, want %q", got, want)
	}
}
