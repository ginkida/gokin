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

	m.openSettings([]SettingItem{
		{Key: "permissions", Desc: "a", On: true},
		{Key: "sandbox", Desc: "b", On: false},
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
