package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SettingItem is one toggle shown in the /settings modal. Mirrors
// commands.ToggleState (the app builds these from it) so the modal and /set
// share one source of truth for what is configurable.
type SettingItem struct {
	Key  string
	Desc string
	On   bool
}

// OpenSettingsMsg opens the interactive settings screen with the given toggles
// plus the current model/provider for the header.
type OpenSettingsMsg struct {
	Items    []SettingItem
	Model    string
	Provider string
}

// SetSettingToggleCallback wires the app handler invoked when the user flips a
// setting in the modal. It receives the key and the new value; the app applies
// it live via ApplyConfig.
func (m *Model) SetSettingToggleCallback(cb func(key string, on bool)) {
	m.onSettingToggle = cb
}

// openSettings enters the settings modal with a fresh snapshot of toggles + the
// current model/provider for the header.
func (m *Model) openSettings(msg OpenSettingsMsg) {
	m.settingsItems = msg.Items
	m.settingsCursor = 0
	m.settingsModel = msg.Model
	m.settingsProvider = msg.Provider
	m.state = StateSettings
}

// handleSettingsKeys drives the settings modal: navigate with ↑/↓, flip the
// selected toggle with space/enter (applies live), close with Esc.
func (m *Model) handleSettingsKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
		}
	case "down", "j":
		if m.settingsCursor < len(m.settingsItems)-1 {
			m.settingsCursor++
		}
	case "enter", " ":
		if m.settingsCursor >= 0 && m.settingsCursor < len(m.settingsItems) {
			item := &m.settingsItems[m.settingsCursor]
			item.On = !item.On // optimistic flip so the UI updates immediately
			if m.onSettingToggle != nil {
				m.onSettingToggle(item.Key, item.On)
			}
		}
	case "m":
		// Jump straight to the model selector (the toggles are all bool; model
		// is a list, so it reuses the existing selector rather than a checkbox).
		m.openModelSelector()
		return nil
	case "esc", "q":
		m.state = StateInput
		return m.input.Focus()
	}
	return nil
}

// renderSettings draws the settings modal.
func (m Model) renderSettings() string {
	var b strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	b.WriteString(titleStyle.Render("Settings"))
	b.WriteString("  ")
	b.WriteString(subtitleStyle.Render("/settings"))
	b.WriteString("\n\n")

	// Model/provider header — shown here so the modal is the one place that
	// surfaces the whole picture; model is changed with 'm', provider via /provider.
	if m.settingsModel != "" || m.settingsProvider != "" {
		hdr := lipgloss.NewStyle().Foreground(ColorMuted)
		fmt.Fprintf(&b, "  %s\n\n", hdr.Render(fmt.Sprintf("Model: %s   ·   Provider: %s", m.settingsModel, m.settingsProvider)))
	}

	if len(m.settingsItems) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorMuted).Italic(true).Width(paletteWidth - 4)
		b.WriteString(emptyStyle.Render("  No settings available."))
		b.WriteString("\n\n")
		return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
	}

	for i, item := range m.settingsItems {
		prefix := "  "
		style := m.styles.ModalNormal
		if i == m.settingsCursor {
			prefix = "> "
			style = m.styles.ModalSelected
		}

		box := "[ ]"
		if item.On {
			box = "[✓]"
		}
		label := fmt.Sprintf("%s %-12s %s", box, item.Key, onOffLabel(item.On))
		fmt.Fprintf(&b, "%s%s\n", prefix, style.Render(label))

		descStyle := m.styles.ModalMuted.Width(paletteWidth - 8)
		fmt.Fprintf(&b, "      %s\n", descStyle.Render(item.Desc))
	}

	b.WriteString("\n")
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true).Align(lipgloss.Center).Width(paletteWidth - 4)
	b.WriteString(footerStyle.Render("↑/↓ Navigate  ·  Space Toggle  ·  m Model  ·  /provider to switch  ·  Esc Close"))
	b.WriteString("\n")

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}

func onOffLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
