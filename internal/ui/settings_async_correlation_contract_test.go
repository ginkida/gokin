package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSettingToggleLateResultCannotSettleNewerRequestAfterReopen(t *testing.T) {
	m := NewModel()
	m.width = 80
	type request struct {
		id  string
		key string
		on  bool
	}
	var requests []request
	m.SetSettingToggleCallbackWithID(func(requestID, key string, on bool) {
		requests = append(requests, request{id: requestID, key: key, on: on})
	})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "sandbox", Name: "Sandbox", On: false, Live: true}}})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if len(requests) != 1 || requests[0].id == "" || requests[0].key != "sandbox" || !requests[0].on {
		t.Fatalf("first toggle request=%+v", requests)
	}
	firstID := requests[0].id
	_ = m.handleMessageTypes(SettingToggleResultMsg{
		RequestID: firstID,
		Key:       "sandbox",
		On:        true,
		Success:   true,
	})

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if len(requests) != 2 || requests[1].id == "" || requests[1].id == firstID || requests[1].on {
		t.Fatalf("second toggle request=%+v", requests)
	}
	secondID := requests[1].id

	_ = m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})
	m.openSettings(OpenSettingsMsg{Items: []SettingItem{{Key: "sandbox", Name: "Sandbox", On: true, Live: true}}})
	if !m.settingsItems[0].Pending || m.settingsItems[0].On {
		t.Fatalf("reopened modal lost newer optimistic request: %+v", m.settingsItems[0])
	}

	// A duplicate/late failure for the first request must not clear, roll back,
	// or replace feedback for the second request merely because the key matches.
	_ = m.handleMessageTypes(SettingToggleResultMsg{
		RequestID: firstID,
		Key:       "sandbox",
		On:        true,
		Success:   false,
		Message:   "old request failed",
	})
	if !m.settingsItems[0].Pending || m.settingsItems[0].On || len(m.settingsPending) != 1 {
		t.Fatalf("late first result settled newer request: item=%+v pending=%v", m.settingsItems[0], m.settingsPending)
	}
	if strings.Contains(m.settingsNotice, "old request") ||
		strings.Contains(stripAnsi(m.output.Content()), "old request") {
		t.Fatalf("late first result leaked stale feedback: notice=%q output=%q", m.settingsNotice, stripAnsi(m.output.Content()))
	}

	_ = m.handleMessageTypes(SettingToggleResultMsg{
		RequestID: secondID,
		Key:       "sandbox",
		On:        false,
		Success:   true,
	})
	if m.settingsItems[0].Pending || m.settingsItems[0].On || len(m.settingsPending) != 0 {
		t.Fatalf("active result did not settle request: item=%+v pending=%v", m.settingsItems[0], m.settingsPending)
	}
}

func TestConfigUpdateIgnoresOlderOrUnversionedSnapshot(t *testing.T) {
	m := NewModel()

	_ = m.handleMessageTypes(ConfigUpdateMsg{
		Revision:           12,
		PermissionsEnabled: true,
		SandboxEnabled:     true,
		CompactMode:        true,
		ReducedMotion:      true,
		ShowTokenUsage:     true,
		ModelName:          "new-model",
	})
	for _, stale := range []ConfigUpdateMsg{
		{
			Revision:           11,
			PermissionsEnabled: false,
			SandboxEnabled:     false,
			CompactMode:        false,
			ReducedMotion:      false,
			ShowTokenUsage:     false,
			ModelName:          "old-model",
		},
		{
			PermissionsEnabled: false,
			SandboxEnabled:     false,
			CompactMode:        false,
			ReducedMotion:      false,
			ShowTokenUsage:     false,
			ModelName:          "legacy-old-model",
		},
	} {
		_ = m.handleMessageTypes(stale)
	}

	if !m.permissionsEnabled || !m.sandboxEnabled || !m.CompactMode ||
		!m.reducedMotion || !m.showTokens || m.currentModel != "new-model" {
		t.Fatalf("stale config snapshot rolled back UI: permissions=%v sandbox=%v compact=%v motion=%v tokens=%v model=%q",
			m.permissionsEnabled, m.sandboxEnabled, m.CompactMode, m.reducedMotion, m.showTokens, m.currentModel)
	}
}
