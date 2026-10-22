package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRapidDistinctFollowupsSubmitWithoutArtificialDelay(t *testing.T) {
	for _, state := range []State{StateInput, StateProcessing, StateStreaming} {
		t.Run(state.String(), func(t *testing.T) {
			m := *NewModel()
			m.SetKeyboardProfile("claude")
			m.minSubmitDelay = time.Hour // This verifies message identity, not scheduler timing.
			var submitted []string
			m.SetCallbacks(func(s string) { submitted = append(submitted, s) }, nil)
			m.input.textarea.SetValue("first request")
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			m.state = state
			m.input.textarea.SetValue("different follow-up")
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if len(submitted) != 2 || submitted[1] != "different follow-up" || m.input.Value() != "" {
				t.Fatalf("distinct follow-up blocked: sent=%v draft=%q", submitted, m.input.Value())
			}
			if m.state != StateProcessing && m.state != state {
				t.Fatalf("submit changed active state: %v", m.state)
			}
			// Recalled duplicate submits remain protected, and never insert a newline.
			m.input.textarea.SetValue("different follow-up")
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(Model)
			if len(submitted) != 2 || m.input.textarea.Value() != "different follow-up" {
				t.Fatal("duplicate guard lost ownership")
			}
		})
	}
}

func TestSubmitDebounceComparesPasteContentsNotChipLabels(t *testing.T) {
	m := *NewModel()
	m.minSubmitDelay = time.Hour
	var submitted []string
	m.SetCallbacks(func(s string) { submitted = append(submitted, s) }, nil)
	for _, word := range []string{"first", "second"} {
		paste := strings.Repeat(word+"\n", 20)
		m.input.setRecalledValue(paste)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
	}
	if len(submitted) != 2 || !strings.HasPrefix(submitted[0], "first") || !strings.HasPrefix(submitted[1], "second") {
		t.Fatalf("different pastes sharing a display label were debounced: %d submissions", len(submitted))
	}
}
