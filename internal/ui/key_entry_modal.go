package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// OpenKeyEntryMsg opens the masked API-key entry modal for a provider. Sent by
// the app when /login <provider> is run with no key on the line.
type OpenKeyEntryMsg struct {
	Provider    string
	DisplayName string
	SetupURL    string
}

// SetKeyEntrySubmitCallback wires the app handler invoked when the user submits
// a key in the modal. It receives the provider and the entered key; the app
// applies it (re-invoking /login) — the key is never echoed to the model.
func (m *Model) SetKeyEntrySubmitCallback(cb func(provider, key string)) {
	m.onKeyEntrySubmit = cb
}

// openKeyEntry enters the masked key-entry modal.
func (m *Model) openKeyEntry(msg OpenKeyEntryMsg) tea.Cmd {
	ti := textinput.New()
	ti.EchoMode = textinput.EchoPassword // mask the secret as it's typed
	ti.Placeholder = "paste your API key"
	ti.CharLimit = 400
	if m.width > 12 {
		ti.Width = m.width - 12
	}
	m.keyEntryInput = ti
	m.keyEntryProvider = msg.Provider
	m.keyEntryDisplayName = msg.DisplayName
	if m.keyEntryDisplayName == "" {
		m.keyEntryDisplayName = msg.Provider
	}
	m.keyEntrySetupURL = msg.SetupURL
	m.state = StateAPIKeyEntry
	return m.keyEntryInput.Focus()
}

// handleKeyEntryKeys drives the masked key-entry modal: Enter submits the key
// (never reaches the model), Esc cancels, everything else types into the masked
// field.
func (m *Model) handleKeyEntryKeys(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEnter:
		key := strings.TrimSpace(m.keyEntryInput.Value())
		provider := m.keyEntryProvider
		m.closeKeyEntry()
		if key != "" && m.onKeyEntrySubmit != nil {
			m.onKeyEntrySubmit(provider, key)
		}
		return m.input.Focus()
	case tea.KeyEsc:
		m.closeKeyEntry()
		if m.toastManager != nil {
			m.toastManager.ShowInfo("Login cancelled")
		}
		return m.input.Focus()
	}

	var cmd tea.Cmd
	m.keyEntryInput, cmd = m.keyEntryInput.Update(msg)
	return cmd
}

func (m *Model) closeKeyEntry() {
	m.keyEntryInput.Reset()
	m.keyEntryProvider = ""
	m.keyEntryDisplayName = ""
	m.keyEntrySetupURL = ""
	m.state = StateInput
}

// renderKeyEntry draws the masked key-entry modal.
func (m Model) renderKeyEntry() string {
	var b strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted).Width(paletteWidth - 4)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	b.WriteString(titleStyle.Render("Set " + m.keyEntryDisplayName + " API key"))
	b.WriteString("\n\n")

	if m.keyEntrySetupURL != "" {
		b.WriteString(mutedStyle.Render("Get a key at " + m.keyEntrySetupURL))
		b.WriteString("\n\n")
	}

	b.WriteString("  ")
	b.WriteString(m.keyEntryInput.View())
	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("The key is masked and never sent to the model."))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("Enter Save  ·  Esc Cancel"))
	b.WriteString("\n")

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}
