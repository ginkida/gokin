package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEscapeReturnsSessionStartPromptAndPreservesNewerDraft(t *testing.T) {
	for _, draft := range []string{"", "newer draft"} {
		t.Run(draft, func(t *testing.T) {
			m := *NewModel()
			m.width, m.height, m.state = 120, 40, StateProcessing
			m.input.textarea.SetValue(draft)
			cancelled := false
			m.onCancel = func() { cancelled = true }
			m.handleMessageTypes(SessionStartWaitMsg{ID: 1, Prompt: "original prompt", Waiting: true})
			m.handleMessageTypes(SessionStartWaitMsg{ID: 0})
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
			result := updated.(Model)
			want := draft
			if want == "" {
				want = "original prompt"
			}
			if !cancelled || result.state != StateInput || result.input.Value() != want || result.sessionStartText != "" {
				t.Fatal("Esc lost pending prompt/draft or did not cancel its wait", result.input.Value())
			}
		})
	}
}
