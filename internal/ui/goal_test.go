package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEscapeCancelsIdleGoalWaitAndPreservesDraftEditing(t *testing.T) {
	for _, draft := range []string{"", "keep this draft"} {
		t.Run(draft, func(t *testing.T) {
			m := *NewModel()
			m.width, m.height, m.state = 120, 40, StateInput
			m.runtimeStatus = RuntimeStatusSnapshot{GoalActive: true, GoalPaused: true, GoalWaiting: true}
			m.input.textarea.SetValue(draft)
			cancelled := 0
			m.SetCancelCallback(func() { cancelled++ })
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			result := updated.(Model)
			if draft == "" {
				if cancelled != 1 || result.runtimeStatus.GoalWaiting || result.state != StateInput || !result.lastRewindEscape.IsZero() {
					t.Fatal("Esc did not cancel the idle goal wakeup cleanly")
				}
			} else if cancelled != 0 || result.input.Value() != draft {
				t.Fatal("goal cancellation took over the draft-editing gesture")
			}
		})
	}
}
