package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestReadOnlyOverlayShortcutsRemainReachableDuringActiveTurn(t *testing.T) {
	tests := []struct {
		name      string
		key       tea.KeyMsg
		wantState State
	}{
		{
			name:      "notification center",
			key:       tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true},
			wantState: StateNotificationCenter,
		},
		{
			name:      "context observatory",
			key:       tea.KeyMsg{Type: tea.KeyCtrlH},
			wantState: StateContextObservatory,
		},
	}

	for _, active := range []struct {
		name  string
		state State
	}{
		{name: "processing", state: StateProcessing},
		{name: "streaming", state: StateStreaming},
	} {
		for _, tt := range tests {
			t.Run(tt.name+"/"+active.name, func(t *testing.T) {
				m := *NewModel()
				m.state = active.state
				m.input.textarea.SetValue("follow-up draft")
				m.input.textarea.CursorEnd()

				updated, _ := m.Update(tt.key)
				opened := updated.(Model)
				if opened.state != tt.wantState {
					t.Fatalf("shortcut state=%v, want %v", opened.state, tt.wantState)
				}
				if got := opened.activeRequestState(); got != active.state {
					t.Fatalf("overlay lost active request state=%v, want %v", got, active.state)
				}
				if got := opened.input.textarea.Value(); got != "follow-up draft" {
					t.Fatalf("shortcut changed type-ahead draft=%q", got)
				}

				closedModel, _ := opened.Update(tea.KeyMsg{Type: tea.KeyEscape})
				closed := closedModel.(Model)
				if closed.state != active.state {
					t.Fatalf("close state=%v, want %v", closed.state, active.state)
				}
				if got := closed.input.textarea.Value(); got != "follow-up draft" {
					t.Fatalf("close changed type-ahead draft=%q", got)
				}
			})
		}
	}
}
