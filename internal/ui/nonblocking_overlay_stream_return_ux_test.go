package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

type composerOverlayLifecycleCase struct {
	name      string
	wantState State
	open      func(Model) Model
	close     func(Model) Model
}

func composerOverlayLifecycleCases() []composerOverlayLifecycleCase {
	updateKey := func(m Model, msg tea.KeyMsg) Model {
		updated, _ := m.Update(msg)
		return updated.(Model)
	}

	return []composerOverlayLifecycleCase{
		{
			name:      "command palette",
			wantState: StateCommandPalette,
			open: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlP})
			},
			close: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyEscape})
			},
		},
		{
			name:      "notification center",
			wantState: StateNotificationCenter,
			open: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}, Alt: true})
			},
			close: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyEscape})
			},
		},
		{
			name:      "shortcuts",
			wantState: StateShortcutsOverlay,
			open: func(m Model) Model {
				m.ShowCommandPalette()
				_ = m.dispatchPaletteAction(paletteActionShortcuts)
				return m
			},
			close: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyEscape})
			},
		},
		{
			name:      "direct model selector",
			wantState: StateModelSelector,
			open: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlK})
			},
			close: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyEscape})
			},
		},
		{
			name:      "context observatory",
			wantState: StateContextObservatory,
			open: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyCtrlH})
			},
			close: func(m Model) Model {
				return updateKey(m, tea.KeyMsg{Type: tea.KeyEscape})
			},
		},
	}
}

func TestNonBlockingOverlaysRestoreLiveStreamAndComposer(t *testing.T) {
	streamMessages := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "text", msg: StreamTextMsg("still working")},
		{name: "thinking", msg: StreamThinkingMsg("still reasoning")},
	}

	for _, overlayCase := range composerOverlayLifecycleCases() {
		for _, stream := range streamMessages {
			t.Run(overlayCase.name+"/"+stream.name, func(t *testing.T) {
				m := *NewModel()
				m.state = StateInput
				m.input.textarea.SetValue("follow-up draft")
				m.input.textarea.CursorEnd()

				overlay := overlayCase.open(m)
				if overlay.state != overlayCase.wantState {
					t.Fatalf("open state=%v, want %v", overlay.state, overlayCase.wantState)
				}

				updated, _ := overlay.Update(stream.msg)
				overlay = updated.(Model)
				if overlay.state != overlayCase.wantState {
					t.Fatalf("stream clobbered overlay: state=%v, want %v", overlay.state, overlayCase.wantState)
				}

				closed := overlayCase.close(overlay)
				if closed.state != StateStreaming {
					t.Fatalf("close returned state=%v, want live StateStreaming", closed.state)
				}
				if got := closed.input.textarea.Value(); got != "follow-up draft" {
					t.Fatalf("close changed composer draft=%q", got)
				}
				if !closed.input.Focused() {
					t.Fatal("close did not restore main composer focus")
				}
			})
		}
	}
}

func TestNonBlockingOverlaysDoNotResurrectSettledStream(t *testing.T) {
	for _, overlayCase := range composerOverlayLifecycleCases() {
		t.Run(overlayCase.name, func(t *testing.T) {
			m := *NewModel()
			m.state = StateInput
			m.input.textarea.SetValue("follow-up draft")
			m.input.textarea.CursorEnd()

			overlay := overlayCase.open(m)
			updated, _ := overlay.Update(StreamTextMsg("brief response"))
			overlay = updated.(Model)
			updated, _ = overlay.Update(ResponseDoneMsg{})
			overlay = updated.(Model)
			if overlay.state != overlayCase.wantState {
				t.Fatalf("completion clobbered overlay: state=%v, want %v", overlay.state, overlayCase.wantState)
			}

			closed := overlayCase.close(overlay)
			if closed.state != StateInput {
				t.Fatalf("close resurrected settled stream: state=%v, want StateInput", closed.state)
			}
			if got := closed.input.textarea.Value(); got != "follow-up draft" {
				t.Fatalf("close changed composer draft=%q", got)
			}
			if !closed.input.Focused() {
				t.Fatal("close did not restore main composer focus")
			}
		})
	}
}
