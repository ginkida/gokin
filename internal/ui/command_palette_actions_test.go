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

func TestCommandPaletteHandlerWiresPageAndBoundaryKeys(t *testing.T) {
	m := NewModel()
	m.commandPalette.commands = paletteGeometryCommands(30)
	m.commandPalette.visible = true
	m.commandPalette.filterCommands("")
	m.commandPalette.SetSize(60, 18)
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.commandPalette.selected != 8 {
		t.Fatalf("PgDown selected=%d, want 8", m.commandPalette.selected)
	}
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.commandPalette.selected != 29 {
		t.Fatalf("End selected=%d, want 29", m.commandPalette.selected)
	}
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyHome})
	if m.commandPalette.selected != 0 {
		t.Fatalf("Home selected=%d, want 0", m.commandPalette.selected)
	}
}

func TestCommandPaletteEnterOnNoMatchOrDisabledStaysOpen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command EnhancedPaletteCommand
		query   string
	}{
		{name: "no match", command: EnhancedPaletteCommand{Name: "help", Shortcut: "/help", Enabled: true}, query: "missing"},
		{name: "disabled", command: EnhancedPaletteCommand{Name: "locked", Shortcut: "/locked", Enabled: false}, query: "locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.commandPalette.commands = []EnhancedPaletteCommand{tc.command}
			m.commandPalette.visible = true
			m.commandPalette.SetQuery(tc.query)
			m.state = StateCommandPalette

			_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
			if m.state != StateCommandPalette || !m.commandPalette.IsVisible() {
				t.Fatalf("Enter changed palette state: state=%v visible=%v", m.state, m.commandPalette.IsVisible())
			}
			if m.commandPalette.GetQuery() != tc.query {
				t.Fatalf("Enter discarded query: got %q, want %q", m.commandPalette.GetQuery(), tc.query)
			}
		})
	}
}
