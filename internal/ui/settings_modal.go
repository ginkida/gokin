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
	Key      string
	Name     string // friendly display name (falls back to Key if empty)
	Desc     string
	Category string // group header; items are category-contiguous (one source of truth)
	On       bool
	// Live false means the flip persists but applies on next launch; the modal
	// shows a "restart to apply" hint so it is never a silent no-op.
	Live bool
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

	// Build the flat row list: a header row when the category changes, then ONE
	// row per item. Headers are render-only — the cursor indexes items (0..N-1),
	// so headers can never desync it.
	type settingRow struct {
		header  string
		itemIdx int // -1 marks a header row
	}
	rows := make([]settingRow, 0, len(m.settingsItems)+6)
	lastCat := ""
	for i, item := range m.settingsItems {
		if item.Category != "" && item.Category != lastCat {
			rows = append(rows, settingRow{header: item.Category, itemIdx: -1})
			lastCat = item.Category
		}
		rows = append(rows, settingRow{itemIdx: i})
	}

	// Height-aware window so the modal never clips on a short terminal: show at
	// most `avail` rows, centered on the selected item. max(6, …) floors it for
	// the pre-first-WindowSizeMsg case (m.height == 0).
	avail := max(6, m.height-12)
	selRow := 0
	for ri, r := range rows {
		if r.itemIdx == m.settingsCursor {
			selRow = ri
			break
		}
	}
	start := 0
	if len(rows) > avail {
		start = selRow - avail/2
		if start < 0 {
			start = 0
		}
		if start > len(rows)-avail {
			start = len(rows) - avail
		}
	}
	end := min(start+avail, len(rows))

	catStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorMuted)
	restartStyle := lipgloss.NewStyle().Foreground(ColorDim)
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	if start > 0 {
		fmt.Fprintf(&b, "  %s\n", moreStyle.Render("↑ more"))
	}
	for ri := start; ri < end; ri++ {
		r := rows[ri]
		if r.itemIdx < 0 {
			fmt.Fprintf(&b, "  %s\n", catStyle.Render(strings.ToUpper(r.header)))
			continue
		}
		item := m.settingsItems[r.itemIdx]
		prefix := "  "
		style := m.styles.ModalNormal
		if r.itemIdx == m.settingsCursor {
			prefix = "> "
			style = m.styles.ModalSelected
		}
		box := "[ ]"
		if item.On {
			box = "[✓]"
		}
		name := item.Name
		if name == "" {
			name = item.Key
		}
		label := fmt.Sprintf("%s %-28s %s", box, truncateForWidth(name, 28), onOffLabel(item.On))
		line := prefix + style.Render(label)
		if !item.Live {
			line += restartStyle.Render("  · restart")
		}
		fmt.Fprintf(&b, "%s\n", line)
	}
	if end < len(rows) {
		fmt.Fprintf(&b, "  %s\n", moreStyle.Render("↓ more"))
	}

	// Description of the SELECTED item only — keeps the list compact (one line
	// each) while still explaining what the cursor is on.
	if m.settingsCursor >= 0 && m.settingsCursor < len(m.settingsItems) {
		descStyle := m.styles.ModalMuted.Width(max(10, paletteWidth-4))
		fmt.Fprintf(&b, "\n  %s\n", descStyle.Render(truncateForWidth(m.settingsItems[m.settingsCursor].Desc, max(10, paletteWidth-4))))
	}

	b.WriteString("\n")
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true).Align(lipgloss.Center).Width(paletteWidth - 4)
	b.WriteString(footerStyle.Render("↑/↓ Navigate  ·  Space Toggle  ·  m Model  ·  /provider to switch  ·  Esc Close"))
	b.WriteString("\n")
	// Quick presets are the one-tap "simpler" surface — a calm dim line so modal
	// users discover them without leaving for /set.
	b.WriteString(footerStyle.Render("Quick presets:  /set preset safe · balanced · fast"))
	b.WriteString("\n")

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}

func onOffLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
