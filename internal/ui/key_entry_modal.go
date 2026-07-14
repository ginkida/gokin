package ui

import (
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// OpenKeyEntryMsg opens the masked API-key entry modal for a provider. Sent by
// the app when /login <provider> is run with no key on the line.
type OpenKeyEntryMsg struct {
	Provider    string
	DisplayName string
	SetupURL    string
}

// KeyEntryResultMsg resolves a masked-key submission without ever carrying the
// secret back through the UI event loop. Warning means the key is active for
// this session but persistence or another non-fatal step needs attention.
type KeyEntryResultMsg struct {
	Provider string
	Success  bool
	Warning  bool
	Message  string
}

// SetKeyEntrySubmitCallback wires the app handler invoked when the user submits
// a key in the modal. It receives the provider and the entered key; the app
// applies it (re-invoking /login) — the key is never echoed to the model.
func (m *Model) SetKeyEntrySubmitCallback(cb func(provider, key string)) {
	m.onKeyEntrySubmit = cb
}

// openKeyEntry enters the masked key-entry modal.
func (m *Model) openKeyEntry(msg OpenKeyEntryMsg) tea.Cmd {
	if m.keyEntryPendingProvider != "" {
		if m.toastManager != nil {
			m.toastManager.ShowWarning("Wait for the current login attempt to finish")
		}
		return nil
	}
	ti := textinput.New()
	ti.EchoMode = textinput.EchoPassword // mask the secret as it's typed
	ti.EchoCharacter = '•'
	ti.Placeholder = "paste your API key"
	ti.CharLimit = 400
	ti.Width = keyEntryInputWidth(m.width)
	m.keyEntryInput = ti
	m.keyEntryProvider = safeKeyEntryProvider(msg.Provider)
	m.keyEntryDisplayName = safeKeyEntryText(msg.DisplayName)
	if m.keyEntryDisplayName == "" {
		m.keyEntryDisplayName = safeKeyEntryText(msg.Provider)
	}
	if strings.TrimSpace(m.keyEntryDisplayName) == "" {
		m.keyEntryDisplayName = "provider"
	}
	m.keyEntrySetupURL = safeKeyEntryText(msg.SetupURL)
	m.keyEntryError = ""
	m.state = StateAPIKeyEntry
	if !m.keyEntryAvailable() {
		m.keyEntryError = "Login is unavailable for this provider"
		m.keyEntryInput.Blur()
		return nil
	}
	return m.keyEntryInput.Focus()
}

// handleKeyEntryKeys drives the masked key-entry modal: Enter submits the key
// (never reaches the model), Esc cancels, everything else types into the masked
// field.
func (m *Model) handleKeyEntryKeys(msg tea.KeyMsg) tea.Cmd {
	if !m.keyEntryAvailable() {
		if msg.Type == tea.KeyEsc {
			m.closeKeyEntry()
			if m.toastManager != nil {
				m.toastManager.ShowInfo("Login closed")
			}
			return m.input.Focus()
		}
		return nil
	}
	switch msg.Type {
	case tea.KeyEnter:
		key := normalizeEnteredAPIKey(m.keyEntryInput.Value())
		if key == "" {
			m.keyEntryError = "API key cannot be empty"
			return m.keyEntryInput.Focus()
		}
		if strings.IndexFunc(key, unicode.IsSpace) >= 0 {
			m.keyEntryError = "API key contains whitespace — paste only the key"
			return m.keyEntryInput.Focus()
		}
		if strings.IndexFunc(key, unicode.IsControl) >= 0 {
			m.keyEntryError = "API key contains unsupported control characters"
			return m.keyEntryInput.Focus()
		}
		provider := m.keyEntryProvider
		displayName := m.keyEntryDisplayName
		m.keyEntryPendingProvider = provider
		m.keyEntryPendingDisplayName = displayName
		m.keyEntryPendingSetupURL = m.keyEntrySetupURL
		m.closeKeyEntry()
		if m.toastManager != nil {
			m.toastManager.ShowTagged("login-"+provider, ToastInfo, "Saving "+displayName+" API key…", 15*time.Second)
		}
		m.onKeyEntrySubmit(provider, key)
		return m.input.Focus()
	case tea.KeyEsc:
		m.closeKeyEntry()
		if m.toastManager != nil {
			m.toastManager.ShowInfo("Login cancelled")
		}
		return m.input.Focus()
	}

	m.keyEntryError = ""
	var cmd tea.Cmd
	m.keyEntryInput, cmd = m.keyEntryInput.Update(msg)
	return cmd
}

func (m *Model) keyEntryAvailable() bool {
	return m.keyEntryProvider != "" && m.onKeyEntrySubmit != nil
}

func safeKeyEntryProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" || strings.IndexFunc(provider, unicode.IsSpace) >= 0 || strings.IndexFunc(provider, unicode.IsControl) >= 0 {
		return ""
	}
	return provider
}

func safeKeyEntryText(text string) string {
	text = ansi.Strip(text)
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func normalizeEnteredAPIKey(raw string) string {
	key := strings.TrimSpace(raw)
	if len(key) >= 2 {
		first, last := key[0], key[len(key)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			key = strings.TrimSpace(key[1 : len(key)-1])
		}
	}
	return key
}

func (m *Model) closeKeyEntry() {
	m.keyEntryInput.Reset()
	// Drop the entire textinput model so the UI retains no reachable copy of
	// the submitted/cancelled secret after the modal closes.
	m.keyEntryInput = textinput.Model{}
	m.keyEntryProvider = ""
	m.keyEntryDisplayName = ""
	m.keyEntrySetupURL = ""
	m.keyEntryError = ""
	m.state = StateInput
}

func keyEntryInputWidth(termWidth int) int {
	paletteWidth, _ := promptPaletteWidth(termWidth)
	// Two cells of line indent plus textinput's prompt must fit inside the card.
	return max(1, paletteWidth-6)
}

// renderKeyEntry draws the masked key-entry modal.
func (m Model) renderKeyEntry() string {
	var b strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(1, paletteWidth-4)
	compact := m.height > 0 && m.height < 16

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted).Width(contentWidth)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true).Width(contentWidth)

	displayName := safeKeyEntryText(m.keyEntryDisplayName)
	if displayName == "" {
		displayName = "provider"
	}
	title := "Set " + displayName + " API key"
	headerWidth := paletteWidth
	if !bordered {
		headerWidth = max(m.width, 1)
	}
	if lipgloss.Width(title) > headerWidth {
		title = "Set API key"
	}
	b.WriteString(titleStyle.Render(truncateForWidth(title, headerWidth)))
	if compact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}

	if setupURL := safeKeyEntryText(m.keyEntrySetupURL); setupURL != "" {
		setup := promptWrappedText("Get a key at "+setupURL, contentWidth, map[bool]int{true: 1, false: 2}[compact])
		b.WriteString(mutedStyle.Render(setup))
		if compact {
			b.WriteString("\n")
		} else {
			b.WriteString("\n\n")
		}
	}

	available := m.keyEntryAvailable()
	if available {
		b.WriteString("  ")
		input := m.keyEntryInput
		input.Width = keyEntryInputWidth(m.width)
		b.WriteString(input.View())
		b.WriteString("\n")
	}
	if m.keyEntryError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		errorLine := errorStyle.Render("  ⚠ " + safeKeyEntryText(m.keyEntryError))
		b.WriteString(fitPanelContent(errorLine, paletteWidth))
		b.WriteString("\n")
	}
	if available {
		if !compact {
			b.WriteString("\n")
		}
		b.WriteString(mutedStyle.Render("The key is masked and never sent to the model."))
		b.WriteString("\n")
		b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "Esc Cancel  ·  Enter Save"))
	} else {
		b.WriteString(mutedStyle.Render("No key was captured or stored."))
		b.WriteString("\n")
		b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "Esc Close"))
	}
	b.WriteString("\n")

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}
