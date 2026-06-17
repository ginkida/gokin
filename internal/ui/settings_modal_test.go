package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSettingsModal_NavigateToggleClose exercises the modal: open populates and
// enters StateSettings, ↓ moves the cursor, Space toggles the selected item
// (optimistic flip + callback with the new value), Esc returns to input.
func TestSettingsModal_NavigateToggleClose(t *testing.T) {
	m := NewModel()

	var gotKey string
	var gotOn bool
	var calls int
	m.SetSettingToggleCallback(func(key string, on bool) { gotKey = key; gotOn = on; calls++ })

	m.openSettings(OpenSettingsMsg{
		Items: []SettingItem{
			{Key: "permissions", Desc: "a", On: true},
			{Key: "sandbox", Desc: "b", On: false},
		},
		Model:    "glm-5.2",
		Provider: "glm",
	})
	if m.state != StateSettings {
		t.Fatal("openSettings should enter StateSettings")
	}

	// Down to the second item.
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyDown})
	if m.settingsCursor != 1 {
		t.Fatalf("cursor=%d, want 1", m.settingsCursor)
	}

	// Space toggles the selected ("sandbox") on.
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if calls != 1 || gotKey != "sandbox" || !gotOn {
		t.Errorf("toggle callback: calls=%d key=%q on=%v, want 1/sandbox/true", calls, gotKey, gotOn)
	}
	if !m.settingsItems[1].On {
		t.Error("selected item should be optimistically flipped on")
	}

	// Esc closes back to the input state.
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Errorf("esc should return to StateInput, got %v", m.state)
	}
}

// TestSettingsModal_MOpensModelSelector: 'm' jumps to the model selector (model
// is a list, not a checkbox, so it reuses the existing selector).
func TestSettingsModal_MOpensModelSelector(t *testing.T) {
	m := NewModel()
	m.SetAvailableModels([]ModelInfo{{ID: "glm-5.2", Name: "GLM 5.2"}})
	m.SetCurrentModel("glm-5.2")
	m.openSettings(OpenSettingsMsg{
		Items:    []SettingItem{{Key: "permissions", On: true}},
		Model:    "glm-5.2",
		Provider: "glm",
	})
	m.handleSettingsKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if m.state != StateModelSelector {
		t.Errorf("'m' in settings should open the model selector, got %v", m.state)
	}
}
