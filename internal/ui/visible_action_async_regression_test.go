package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func regressionShortcutHints(hints []shortcutHint) string {
	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts, hint.key+" "+hint.desc)
	}
	return strings.Join(parts, " · ")
}

func regressionPaletteAction(t *testing.T, commands []EnhancedPaletteCommand, actionID string) EnhancedPaletteCommand {
	t.Helper()
	for _, command := range commands {
		if command.ActionID == actionID {
			return command
		}
	}
	t.Fatalf("palette action %q not found", actionID)
	return EnhancedPaletteCommand{}
}

func TestUnavailablePermissionDetailsAdvertiseBackAndReturn(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StatePermissionPrompt
	m.permRequest = &PermissionRequestMsg{
		ID:       "req-1",
		ToolName: "bash",
		Args:     map[string]any{"command": "echo hi"},
	}

	// With no response callback, Allow keeps the modal open and enters its
	// durable unavailable state.
	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !isUnavailablePromptNotice(m.permNotice) {
		t.Fatalf("setup: permission prompt did not enter unavailable state: %q", m.permNotice)
	}

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.permShowDetails {
		t.Fatal("setup: ? did not open permission details")
	}

	if hints := regressionShortcutHints(m.contextualShortcutHintPairs()); !strings.Contains(hints, "? Back") || strings.Contains(hints, "? Details") {
		t.Errorf("open unavailable details advertise the wrong ? action: %q", hints)
	}
	if view := stripAnsi(m.renderPermissionPrompt()); !strings.Contains(view, "? Back") {
		t.Errorf("open unavailable details footer omits ? Back:\n%s", view)
	}

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.permShowDetails || m.state != StatePermissionPrompt || m.permRequest == nil {
		t.Errorf("? did not return to the unavailable decision view: details=%v state=%v request=%v", m.permShowDetails, m.state, m.permRequest)
	}
}

func TestOpenPaletteRefreshesCompactDescriptionWithoutLosingContext(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 30
	m.SetCompactMode(false)
	m.ShowCommandPalette()
	m.commandPalette.SetQuery("Toggle")

	compactIndex := -1
	for i, command := range m.commandPalette.filtered {
		if command.ActionID == paletteActionCompactMode {
			compactIndex = i
			break
		}
	}
	if compactIndex < 0 {
		t.Fatal("setup: compact action is not visible for the Toggle query")
	}
	m.commandPalette.selected = compactIndex
	queryBefore := m.commandPalette.GetQuery()
	selectedBefore := m.commandPalette.GetSelected()
	if selectedBefore == nil || selectedBefore.ActionID != paletteActionCompactMode {
		t.Fatalf("setup: selected action=%+v", selectedBefore)
	}
	if description := strings.ToLower(selectedBefore.Description); !strings.Contains(description, "currently normal") {
		t.Fatalf("setup: compact description=%q", description)
	}

	updated, _ := m.Update(ConfigUpdateMsg{CompactMode: true})
	got := updated.(Model)
	if got.state != StateCommandPalette || !got.commandPalette.IsVisible() {
		t.Fatalf("config update closed the palette: state=%v visible=%v", got.state, got.commandPalette.IsVisible())
	}
	if query := got.commandPalette.GetQuery(); query != queryBefore {
		t.Errorf("config update lost palette query: got %q want %q", query, queryBefore)
	}
	selectedAfter := got.commandPalette.GetSelected()
	if selectedAfter == nil || selectedAfter.ActionID != paletteActionCompactMode {
		t.Fatalf("config update lost palette selection: got %+v", selectedAfter)
	}
	if description := strings.ToLower(selectedAfter.Description); !strings.Contains(description, "currently compact — restore") {
		source := strings.ToLower(regressionPaletteAction(t, got.commandPalette.actionCommands, paletteActionCompactMode).Description)
		t.Errorf("open palette kept stale compact direction: visible=%q source=%q", description, source)
	}
}
