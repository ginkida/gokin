package ui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAsyncSettingsReturnsToLiveTurnUntilItSettles(t *testing.T) {
	for _, state := range []State{StateProcessing, StateStreaming} {
		t.Run(activeSurfaceLabel(state), func(t *testing.T) {
			m := NewModel()
			m.state = state
			updated, _ := m.Update(OpenSettingsMsg{Items: []SettingItem{{Key: "sandbox", Name: "Sandbox"}}})
			got := updated.(Model)
			if got.state != StateSettings || got.settingsReturnState != state {
				t.Fatalf("open lost live return state: state=%v return=%v want=%v", got.state, got.settingsReturnState, state)
			}
			_ = got.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})
			if got.state != state {
				t.Fatalf("closing settings faked idle: got=%v want=%v", got.state, state)
			}
		})
	}
}

func TestAsyncKeyEntryReturnsToLiveTurnAndDestroysSecret(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	m.SetKeyEntrySubmitCallback(func(string, string) {})
	updated, _ := m.Update(OpenKeyEntryMsg{Provider: "glm"})
	got := updated.(Model)
	got.keyEntryInput.SetValue("sk-never-retained")

	_ = got.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if got.state != StateProcessing {
		t.Fatalf("closing key entry faked idle: state=%v", got.state)
	}
	if got.keyEntryInput.Value() != "" || got.keyEntryProvider != "" || got.keyEntryReturnState != StateInput {
		t.Fatalf("key entry close retained secret/lifecycle: value=%q provider=%q return=%v", got.keyEntryInput.Value(), got.keyEntryProvider, got.keyEntryReturnState)
	}
}

func TestSettledTurnChangesAsyncModalReturnToInput(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
	}{
		{name: "response done", msg: ResponseDoneMsg{}},
		{name: "error", msg: ErrorMsg(errors.New("request failed"))},
	}

	for _, tt := range tests {
		t.Run("settings "+tt.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateProcessing
			opened, _ := m.Update(OpenSettingsMsg{})
			settled, _ := opened.(Model).Update(tt.msg)
			got := settled.(Model)
			if got.state != StateSettings || got.settingsReturnState != StateInput {
				t.Fatalf("settlement clobbered modal or kept stale return: state=%v return=%v", got.state, got.settingsReturnState)
			}
			_ = got.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})
			if got.state != StateInput {
				t.Fatalf("settled settings returned to stale activity: %v", got.state)
			}
		})

		t.Run("key entry "+tt.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateStreaming
			m.SetKeyEntrySubmitCallback(func(string, string) {})
			opened, _ := m.Update(OpenKeyEntryMsg{Provider: "glm"})
			settled, _ := opened.(Model).Update(tt.msg)
			got := settled.(Model)
			if got.state != StateAPIKeyEntry || got.keyEntryReturnState != StateInput {
				t.Fatalf("settlement clobbered key modal or kept stale return: state=%v return=%v", got.state, got.keyEntryReturnState)
			}
			_ = got.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEscape})
			if got.state != StateInput {
				t.Fatalf("settled key entry returned to stale activity: %v", got.state)
			}
		})
	}
}

func TestNestedModelSelectorCarriesSettledSettingsReturnState(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	opened, _ := m.Update(OpenSettingsMsg{})
	got := opened.(Model)
	_ = got.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if got.state != StateModelSelector || got.modelSelectorReturnState != StateSettings {
		t.Fatalf("model selector did not nest under settings: state=%v return=%v", got.state, got.modelSelectorReturnState)
	}

	settled, _ := got.Update(ResponseDoneMsg{})
	got = settled.(Model)
	_ = got.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if got.state != StateSettings {
		t.Fatalf("selector did not return to settings: %v", got.state)
	}
	_ = got.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if got.state != StateInput {
		t.Fatalf("nested modal resurrected settled processing state: %v", got.state)
	}
}

func TestStreamStartUpdatesAsyncModalReturnState(t *testing.T) {
	tests := []struct {
		name  string
		open  func(*Model)
		state func(*Model) State
	}{
		{
			name:  "settings",
			open:  func(m *Model) { m.openSettings(OpenSettingsMsg{}) },
			state: func(m *Model) State { return m.settingsReturnState },
		},
		{
			name:  "key entry",
			open:  func(m *Model) { m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm"}) },
			state: func(m *Model) State { return m.keyEntryReturnState },
		},
	}

	for _, tt := range tests {
		for _, msg := range []tea.Msg{StreamThinkingMsg("thinking"), StreamTextMsg("answer")} {
			t.Run(tt.name, func(t *testing.T) {
				m := NewModel()
				m.state = StateProcessing
				m.SetKeyEntrySubmitCallback(func(string, string) {})
				tt.open(m)
				modalState := m.state
				updated, _ := m.Update(msg)
				got := updated.(Model)
				if got.state != modalState || tt.state(&got) != StateStreaming {
					t.Fatalf("stream clobbered modal or left stale return: state=%v return=%v", got.state, tt.state(&got))
				}
			})
		}
	}
}

func TestAsyncModalCloseRevealsBackgroundBatchProgress(t *testing.T) {
	tests := []struct {
		name  string
		open  func(*Model)
		close func(*Model)
	}{
		{
			name:  "settings",
			open:  func(m *Model) { m.openSettings(OpenSettingsMsg{}) },
			close: func(m *Model) { _ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
		{
			name:  "key entry",
			open:  func(m *Model) { m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm"}) },
			close: func(m *Model) { _ = m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.state = StateProcessing
			m.SetKeyEntrySubmitCallback(func(string, string) {})
			tt.open(m)
			updated, _ := m.Update(ProgressCompleteMsg{TotalItems: 2, SuccessCount: 1, FailureCount: 1})
			got := updated.(Model)
			if got.state == StateBatchProgress || !got.progressActive {
				t.Fatalf("background progress clobbered modal or was dropped: state=%v active=%v", got.state, got.progressActive)
			}

			tt.close(&got)
			if got.state != StateBatchProgress || got.progressReturnState != StateProcessing {
				t.Fatalf("close did not reveal progress over live turn: state=%v return=%v", got.state, got.progressReturnState)
			}
			settled, _ := got.Update(CloseOverlayMsg{})
			got = settled.(Model)
			if got.state != StateProcessing || got.progressActive {
				t.Fatalf("progress close did not restore live turn: state=%v active=%v", got.state, got.progressActive)
			}
		})
	}
}

func TestNonBlockingOverlayCloseRevealsBackgroundBatchProgress(t *testing.T) {
	tests := []struct {
		name  string
		open  func(*Model)
		close func(*Model)
	}{
		{
			name:  "notification center",
			open:  func(m *Model) { m.openNotificationCenter() },
			close: func(m *Model) { _ = m.handleNotificationCenterKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
		{
			name: "shortcuts",
			open: func(m *Model) {
				m.shortcutsOverlay.Show()
				m.state = StateShortcutsOverlay
			},
			close: func(m *Model) { _ = m.handleShortcutsOverlayKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
		{
			name:  "model selector",
			open:  func(m *Model) { m.openModelSelector() },
			close: func(m *Model) { _ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
		{
			name: "search results",
			open: func(m *Model) {
				m.workspaceOverlayReturnState = StateInput
				m.state = StateSearchResults
			},
			close: func(m *Model) { _ = m.restoreWorkspaceOverlay() },
		},
		{
			name: "command palette",
			open: func(m *Model) {
				m.commandPalette.Show()
				m.state = StateCommandPalette
			},
			close: func(m *Model) { _ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
		{
			name: "context observatory",
			open: func(m *Model) {
				m.observatoryPanel.Show()
				m.state = StateContextObservatory
			},
			close: func(m *Model) { _ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyEscape}) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			tt.open(m)
			updated, _ := m.Update(ProgressCompleteMsg{TotalItems: 1, SuccessCount: 1})
			got := updated.(Model)
			modalState := got.state
			if !got.progressActive || modalState == StateBatchProgress {
				t.Fatalf("progress did not remain behind overlay: state=%v active=%v", modalState, got.progressActive)
			}
			tt.close(&got)
			if got.state != StateBatchProgress || got.progressReturnState != StateInput {
				t.Fatalf("close hid pending progress: state=%v return=%v", got.state, got.progressReturnState)
			}
		})
	}
}
