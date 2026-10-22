package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type KeyboardProfileMsg struct{ Profile string }

// Standalone NewModel retains the original embedding API. Application startup
// explicitly applies UIConfig, whose default is the Claude-style profile.
func (m *Model) SetKeyboardProfile(profile string) {
	m.claudeKeys = !strings.EqualFold(strings.TrimSpace(profile), "gokin")
	m.input.readline = m.claudeKeys
	if m.observatoryPanel != nil {
		m.observatoryPanel.claudeKeys = m.claudeKeys
	}
	if m.planProgressPanel != nil {
		m.planProgressPanel.claudeKeys = m.claudeKeys
	}
	m.input.updatePlaceholder()
	if m.shortcutsOverlay != nil {
		m.shortcutsOverlay.categories = shortcutsForProfile(m.claudeKeys)
		m.shortcutsOverlay.SetSearch(m.shortcutsOverlay.searchQuery)
	}
	if m.hintSystem != nil {
		m.hintSystem.claudeKeys = m.claudeKeys
	}
	if m.commandPalette != nil {
		m.commandPalette.shortcutLabel = m.keyLabel("Ctrl+P")
	}
	m.RegisterPaletteActions()
}

func profileKeyText(text string, claude bool) string {
	if !claude {
		return text
	}
	return strings.NewReplacer("Ctrl+K", "Alt+P", "Ctrl+P", "F1", "Ctrl+S", "F2",
		"Ctrl+E", "Alt+E", "Ctrl+A", "Alt+A", "Ctrl+H", "Alt+H", "Ctrl+G", "Alt+G", "Ctrl+O", "Alt+O", "Ctrl+X", "Alt+V", "ctrl+o", "alt+o").Replace(text)
}

func (m Model) keyLabel(text string) string { return profileKeyText(text, m.claudeKeys) }

func shortcutsForProfile(claude bool) []ShortcutCategory {
	categories := DefaultShortcuts()
	if !claude {
		return categories
	}
	for i := range categories {
		var entries []Shortcut
		for _, entry := range categories[i].Shortcuts {
			joined := strings.Join(entry.Keys, "+")
			if joined == "E" {
				continue
			}
			if joined == "Ctrl+b" || joined == "Ctrl+f" || joined == "Ctrl+u" || joined == "Ctrl+d" {
				continue
			}
			label := profileKeyText(strings.Join(entry.Keys, "+"), true)
			if label == "Ctrl+p" {
				label = "F1"
			}
			entry.Keys = strings.Split(label, "+")
			entry.Description = profileKeyText(entry.Description, true)
			if label == "Ctrl+L" {
				entry.Description = "Redraw screen, preserving conversation"
			}
			if label == "Ctrl+O" {
				entry.Description = "Open searchable conversation transcript"
			}
			entries = append(entries, entry)
		}
		categories[i].Shortcuts = entries
	}
	return append(categories, ShortcutCategory{Name: "Editing & drafts", Shortcuts: []Shortcut{
		{Keys: []string{"Ctrl", "O"}, Description: "Open searchable conversation transcript"},
		{Keys: []string{"Ctrl", "A/E"}, Description: "Start/end of line"},
		{Keys: []string{"Ctrl", "B/F"}, Description: "Move backward/forward one character"},
		{Keys: []string{"Ctrl", "P/N"}, Description: "Previous/next line or history entry"},
		{Keys: []string{"Ctrl", "K/U/W"}, Description: "Cut to end/start of line or previous word"},
		{Keys: []string{"Ctrl", "Y"}, Description: "Paste the last cut text"},
		{Keys: []string{"Ctrl", "_"}, Description: "Undo input editing"},
		{Keys: []string{"Ctrl", "S"}, Description: "Stash/restore draft; swap when both contain text"},
		{Keys: []string{"Ctrl", "G"}, Description: "Edit prompt in $VISUAL / $EDITOR"},
		{Keys: []string{"Ctrl", "D"}, Description: "Delete forward; press twice on empty input to quit"},
	}})
}

func ShortcutsForProfile(profile string) []ShortcutCategory {
	return shortcutsForProfile(!strings.EqualFold(strings.TrimSpace(profile), "gokin"))
}

func (m *Model) handleClaudeKeys(msg tea.KeyMsg) tea.Cmd {
	if m.state == StateContextObservatory && msg.String() == "alt+h" {
		return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlH})
	}
	if !m.typeAheadActive() {
		m.composerChord = false
		return m.handleLegacyGlobalKeys(msg)
	}
	if !msg.Paste {
		if m.composerChord {
			m.composerChord = false
			if msg.Type == tea.KeyCtrlE {
				return m.editPromptExternally()
			}
		}
		switch msg.String() {
		case "e", "E":
			return nil
		case "ctrl+x":
			m.composerChord = true
			return keyConsumed
		case "alt+o":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlO})
		case "alt+v":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlX})
		case "alt+p":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlK})
		case "f1", "alt+x":
			if m.state == StateInput {
				m.ShowCommandPalette()
				return keyConsumed
			}
		case "f2":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlS})
		case "alt+a":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlA})
		case "alt+h":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlH})
		case "alt+g":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlG})
		case "alt+e":
			return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlE})
		case "ctrl+s":
			if m.toastManager != nil {
				m.toastManager.ShowInfo(m.input.toggleStashedDraft())
			} else {
				m.input.toggleStashedDraft()
			}
			return keyConsumed
		case "ctrl+o":
			m.openTranscript()
			return keyConsumed
		case "ctrl+g":
			return m.editPromptExternally()
		case "ctrl+l":
			return tea.ClearScreen
		case "ctrl+c":
			if m.state == StateInput && !m.input.historySearchMode && m.input.textarea.Value() != "" {
				cleared := m.input.snapshot()
				m.input.AddToHistory(m.input.ExpandedValue())
				m.input.Reset()
				m.input.clearedDraft = &cleared
			}
			m.quitConfirmKey = tea.KeyCtrlC
		case "ctrl+d":
			if m.state == StateInput && m.input.textarea.Value() == "" && !m.input.historySearchMode {
				m.quitConfirmKey = tea.KeyCtrlD
				return m.handleLegacyGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlC})
			}
			return nil
		case "ctrl+a", "ctrl+b", "ctrl+e", "ctrl+f", "ctrl+h", "ctrl+k", "ctrl+u", "ctrl+w", "ctrl+p", "ctrl+n", "ctrl+y", "ctrl+_":
			return nil
		}
	}
	return m.handleLegacyGlobalKeys(msg)
}

func (m Model) quitKeyLabel() string {
	if m.quitConfirmKey == tea.KeyCtrlD {
		return "Ctrl+D"
	}
	return "Ctrl+C"
}

func (m Model) quitKeyMatches(msg tea.KeyMsg) bool {
	if m.claudeKeys && m.quitConfirmKey == tea.KeyCtrlD {
		return msg.Type == tea.KeyCtrlD
	}
	return msg.Type == tea.KeyCtrlC
}

func (m Model) quitHint() string {
	return fmt.Sprintf("%s again Quit · any other key Cancel", m.quitKeyLabel())
}

func (m Model) clearOutputShortcut() string {
	if m.claudeKeys {
		return ""
	}
	return "Ctrl+L"
}
