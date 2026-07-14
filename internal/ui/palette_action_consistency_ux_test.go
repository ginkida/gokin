package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func activeToastContains(m *Model, want string) bool {
	if m.toastManager == nil {
		return false
	}
	for _, toast := range m.toastManager.Active() {
		if strings.Contains(toast.Title+" "+toast.Message, want) {
			return true
		}
	}
	return false
}

func TestPaletteAndShiftTabUseTheSameSessionModeCycle(t *testing.T) {
	m := NewModel()
	cycleCalls, legacyCalls := 0, 0
	m.SetSessionModeCycleCallback(func() { cycleCalls++ })
	m.SetPlanningModeToggleCallback(func() { legacyCalls++ })
	m.state = StateCommandPalette

	_ = m.dispatchPaletteAction(paletteActionPlanningMode)
	if cycleCalls != 1 || legacyCalls != 0 || m.state != StateInput {
		t.Fatalf("palette action diverged: cycle=%d legacy=%d state=%v", cycleCalls, legacyCalls, m.state)
	}
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyShiftTab})
	if cycleCalls != 2 || legacyCalls != 0 {
		t.Fatalf("Shift+Tab diverged: cycle=%d legacy=%d", cycleCalls, legacyCalls)
	}

	m.RegisterPaletteActions()
	m.commandPalette.RefreshCommands()
	found := false
	for _, command := range m.commandPalette.commands {
		if command.ActionID == paletteActionPlanningMode {
			found = true
			if command.Name != "Cycle Session Mode" || !strings.Contains(command.Description, "Normal, Plan, and YOLO") || command.Shortcut != "Shift+Tab" {
				t.Fatalf("palette copy does not describe actual cycle: %+v", command)
			}
		}
	}
	if !found {
		t.Fatal("session mode action is missing from palette")
	}
}

func TestSessionModeCycleFallsBackAndExplainsUnavailableState(t *testing.T) {
	m := NewModel()
	legacyCalls := 0
	m.SetPlanningModeToggleCallback(func() { legacyCalls++ })
	m.state = StateInput
	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyShiftTab})
	if legacyCalls != 1 {
		t.Fatalf("legacy planning callback calls=%d, want 1", legacyCalls)
	}

	unavailable := NewModel()
	unavailable.state = StateInput
	_ = unavailable.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyShiftTab})
	if !activeToastContains(unavailable, "Session mode switching is unavailable") {
		t.Fatal("unwired Shift+Tab should explain why nothing changed")
	}
}

func TestSettingsEntryPointsExplainUnavailableState(t *testing.T) {
	shortcut := NewModel()
	shortcut.state = StateInput
	_ = shortcut.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !activeToastContains(shortcut, "Settings are unavailable") {
		t.Fatal("unwired Ctrl+S should explain why settings did not open")
	}

	palette := NewModel()
	palette.state = StateCommandPalette
	_ = palette.dispatchPaletteAction(paletteActionSettings)
	if palette.state != StateInput || !activeToastContains(palette, "Settings are unavailable") {
		t.Fatalf("unwired palette settings action should return with feedback, state=%v", palette.state)
	}
}

func TestClearScreenFeedbackMatchesShortcutAndPalette(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*Model)
	}{
		{
			name: "shortcut",
			run: func(m *Model) {
				m.state = StateInput
				_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlL})
			},
		},
		{
			name: "palette",
			run: func(m *Model) {
				m.state = StateCommandPalette
				_ = m.dispatchPaletteAction(paletteActionClearScreen)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.output.AppendLine("content that should be cleared")
			tc.run(m)
			if !m.output.IsEmpty() {
				t.Fatal("clear action left output content behind")
			}
			if !activeToastContains(m, "Output cleared") {
				t.Fatal("clear action did not confirm completion")
			}
		})
	}
}

func TestUnknownPaletteActionReturnsVisibleFeedback(t *testing.T) {
	m := NewModel()
	m.state = StateCommandPalette
	_ = m.dispatchPaletteAction("missing-action")
	if m.state != StateInput || !activeToastContains(m, "palette action is unavailable") {
		t.Fatalf("unknown action should fail visibly and recover to input, state=%v", m.state)
	}
}
