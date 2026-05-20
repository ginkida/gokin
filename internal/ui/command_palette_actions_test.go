package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCommandPaletteActionExecutesOnce(t *testing.T) {
	m := NewModel()
	count := 0
	m.commandPalette.SetActionCommands([]EnhancedPaletteCommand{
		{
			Name:     "Increment",
			Shortcut: "Ctrl+I",
			Enabled:  true,
			Type:     CommandTypeAction,
			Action: func() {
				count++
			},
		},
	})
	m.commandPalette.Show()
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if count != 1 {
		t.Fatalf("palette action executed %d times, want once", count)
	}
	if m.state != StateInput {
		t.Fatalf("plain action should return to input, state=%v", m.state)
	}
}

func TestCommandPaletteActionCanOpenModelSelector(t *testing.T) {
	m := NewModel()
	m.availableModels = []ModelInfo{{ID: "fast", Name: "Fast"}}
	m.SetCurrentModel("fast")
	m.RegisterPaletteActions()
	m.commandPalette.Show()
	m.commandPalette.SetQuery("open model")
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateModelSelector {
		t.Fatalf("model selector action should preserve selector state, got %v", m.state)
	}
}
