package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEscapeCancelsIdleAsyncHooksWithoutTakingDraftOrModalKeys(t *testing.T) {
	for _, state := range []string{"idle", "draft", "modal", "no-pending-work"} {
		t.Run(state, func(t *testing.T) {
			m := *NewModel()
			m.width, m.height, m.state = 120, 40, StateInput
			if state == "draft" {
				m.input.textarea.SetValue("keep this draft")
			}
			if state == "modal" {
				m.state = StateContextObservatory
			}
			probes := 0
			m.SetIdleAsyncHookCancelCallback(func() bool { probes++; return state != "no-pending-work" })
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
			result := updated.(Model)
			switch state {
			case "idle":
				if probes != 1 || result.state != StateInput || !result.lastRewindEscape.IsZero() {
					t.Fatal("idle Esc missed pending hooks or armed rewind")
				}
			case "draft", "modal":
				if probes != 0 || state == "draft" && result.input.Value() != "keep this draft" {
					t.Fatal("async cancellation took a draft or modal key")
				}
			case "no-pending-work":
				if probes != 1 || result.lastRewindEscape.IsZero() {
					t.Fatal("idle Esc lost its existing rewind gesture")
				}
			}
		})
	}
}
