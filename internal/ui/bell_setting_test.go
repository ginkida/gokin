package ui

import "testing"

func TestBellSettingUsesCompleteConfigSnapshot(t *testing.T) {
	m := *NewModel()
	if !m.bellEnabled {
		t.Fatal("precondition: terminal bell should be enabled by default")
	}

	next, _ := m.Update(ConfigUpdateMsg{Settings: map[string]bool{"bell": false}})
	m = next.(Model)
	if m.bellEnabled {
		t.Fatal("bell=false in the complete settings snapshot was not applied")
	}
	if cmd := m.bellCmd(); cmd != nil {
		t.Fatal("disabled bell should not schedule terminal output")
	}

	next, _ = m.Update(ConfigUpdateMsg{Settings: map[string]bool{"bell": true}})
	m = next.(Model)
	if !m.bellEnabled {
		t.Fatal("bell=true in the complete settings snapshot was not applied")
	}
	if cmd := m.bellCmd(); cmd == nil {
		t.Fatal("enabled bell should schedule terminal output")
	}
}

func TestPartialConfigUpdateDoesNotDisableBell(t *testing.T) {
	m := *NewModel()

	next, _ := m.Update(ConfigUpdateMsg{CompactMode: true})
	m = next.(Model)
	if !m.bellEnabled {
		t.Fatal("partial config update without a bell setting disabled the bell")
	}
}
