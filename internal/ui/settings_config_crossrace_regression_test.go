package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// A versioned config snapshot is authoritative over an optimistic Settings
// request that started from an older revision. This covers the cross-surface
// race where /settings starts A, /set commits B for the same key, then A's
// result reaches the event loop late.
func TestNewerConfigUpdateSupersedesPendingSettingResult(t *testing.T) {
	m := NewModel()
	m.width = 80

	// Preserve the legacy contract until the first versioned snapshot is seen:
	// old integrations and focused UI tests can still send Revision == 0.
	_ = m.handleMessageTypes(ConfigUpdateMsg{SandboxEnabled: true})
	if !m.sandboxEnabled {
		t.Fatal("unversioned config update was not accepted before versioned flow began")
	}
	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 20, SandboxEnabled: false})

	var requestID string
	m.SetSettingToggleCallbackWithID(func(id, key string, on bool) {
		if key != "sandbox" || !on {
			t.Fatalf("toggle callback=(%q, %q, %v), want sandbox on", id, key, on)
		}
		requestID = id
	})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{
		Key:  "sandbox",
		Name: "Sandbox",
		On:   false,
		Live: true,
	}}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if requestID == "" || !m.settingsItems[0].Pending || !m.settingsItems[0].On {
		t.Fatalf("optimistic request was not staged: id=%q item=%+v", requestID, m.settingsItems[0])
	}

	// /set B commits after A started and restores sandbox=off. The newer
	// revision must settle A's optimistic ownership immediately; otherwise the
	// row remains locked and the late A result can repaint stale state.
	_ = m.handleMessageTypes(ConfigUpdateMsg{Revision: 22, SandboxEnabled: false})
	if m.sandboxEnabled || m.settingsItems[0].On || m.settingsItems[0].Pending || len(m.settingsPending) != 0 {
		t.Fatalf("newer config did not supersede optimistic setting: sandbox=%v item=%+v pending=%v",
			m.sandboxEnabled, m.settingsItems[0], m.settingsPending)
	}

	noticeAfterConfig := m.settingsNotice
	outputAfterConfig := stripAnsi(m.output.Content())
	_ = m.handleMessageTypes(SettingToggleResultMsg{
		RequestID: requestID,
		Key:       "sandbox",
		On:        true,
		Success:   true,
		Message:   "stale A says sandbox is on",
	})

	if m.sandboxEnabled || m.settingsItems[0].On || m.settingsItems[0].Pending || len(m.settingsPending) != 0 {
		t.Fatalf("late A result overwrote newer config: sandbox=%v item=%+v pending=%v",
			m.sandboxEnabled, m.settingsItems[0], m.settingsPending)
	}
	if m.settingsNotice != noticeAfterConfig || stripAnsi(m.output.Content()) != outputAfterConfig ||
		strings.Contains(m.settingsNotice, "stale A") || strings.Contains(stripAnsi(m.output.Content()), "stale A") {
		t.Fatalf("late A result leaked stale feedback: notice=%q output=%q", m.settingsNotice, stripAnsi(m.output.Content()))
	}
}
