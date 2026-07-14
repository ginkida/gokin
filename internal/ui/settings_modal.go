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
	// Pending is UI-owned transient state while the async ApplyConfig worker
	// confirms the optimistic flip. It is never supplied by the app snapshot.
	Pending bool
}

// OpenSettingsMsg opens the interactive settings screen with the given toggles
// plus the current model/provider for the header.
type OpenSettingsMsg struct {
	Items    []SettingItem
	Model    string
	Provider string
}

// SettingToggleResultMsg resolves an optimistic settings flip. On failure On
// carries the authoritative restored value so the modal cannot remain visually
// ahead of the actual runtime/config state.
type SettingToggleResultMsg struct {
	Key     string
	On      bool
	Success bool
	Message string
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
	m.settingsItems = append([]SettingItem(nil), msg.Items...)
	for i := range m.settingsItems {
		m.settingsItems[i].Key = strings.ToLower(strings.TrimSpace(m.settingsItems[i].Key))
		m.settingsItems[i].Name = safeKeyEntryText(m.settingsItems[i].Name)
		m.settingsItems[i].Desc = safeKeyEntryText(m.settingsItems[i].Desc)
		m.settingsItems[i].Category = safeKeyEntryText(m.settingsItems[i].Category)
		if pendingOn, ok := m.settingsPending[m.settingsItems[i].Key]; ok {
			// A fresh app snapshot can arrive while an earlier ApplyConfig worker
			// is still running. Preserve the optimistic row and its interaction
			// lock instead of making the same key look toggleable again.
			m.settingsItems[i].On = pendingOn
			m.settingsItems[i].Pending = true
		}
	}
	m.settingsCursor = 0
	m.settingsModel = safeKeyEntryText(msg.Model)
	m.settingsProvider = safeKeyEntryText(msg.Provider)
	m.settingsNotice = ""
	m.state = StateSettings
}

// handleSettingsKeys drives the settings modal: navigate with ↑/↓, flip the
// selected toggle with space/enter (applies live), close with Esc.
func (m *Model) handleSettingsKeys(msg tea.KeyMsg) tea.Cmd {
	if len(m.settingsItems) == 0 {
		m.settingsCursor = 0
	} else {
		m.settingsCursor = min(max(m.settingsCursor, 0), len(m.settingsItems)-1)
	}
	page := settingsItemVisibleCount(m.height, len(m.settingsItems))
	switch msg.String() {
	case "up", "k":
		if m.settingsCursor > 0 {
			m.settingsCursor--
			m.settingsNotice = ""
		}
	case "down", "j":
		if m.settingsCursor < len(m.settingsItems)-1 {
			m.settingsCursor++
			m.settingsNotice = ""
		}
	case "home", "g":
		m.settingsCursor = 0
		m.settingsNotice = ""
	case "end", "G":
		if len(m.settingsItems) > 0 {
			m.settingsCursor = len(m.settingsItems) - 1
		}
		m.settingsNotice = ""
	case "pgup":
		m.settingsCursor = max(m.settingsCursor-page, 0)
		m.settingsNotice = ""
	case "pgdown":
		if len(m.settingsItems) > 0 {
			m.settingsCursor = min(m.settingsCursor+page, len(m.settingsItems)-1)
		}
		m.settingsNotice = ""
	case "enter", " ":
		if m.settingsCursor >= 0 && m.settingsCursor < len(m.settingsItems) {
			if len(m.settingsPending) > 0 {
				m.settingsNotice = "Wait for the current setting to finish applying"
				return nil
			}
			item := &m.settingsItems[m.settingsCursor]
			if m.onSettingToggle == nil {
				m.settingsNotice = "Changes are unavailable in this session"
				return nil
			}
			if item.Key == "" {
				m.settingsNotice = "This setting has no configurable key"
				return nil
			}
			item.On = !item.On // optimistic value, explicitly marked pending below
			item.Pending = true
			if m.settingsPending == nil {
				m.settingsPending = make(map[string]bool)
			}
			m.settingsPending[item.Key] = item.On
			m.onSettingToggle(item.Key, item.On)
			name := item.Name
			if name == "" {
				name = item.Key
			}
			m.settingsNotice = fmt.Sprintf("%s: applying…", name)
		}
	case "m":
		// Jump straight to the model selector (the toggles are all bool; model
		// is a list, so it reuses the existing selector rather than a checkbox).
		m.settingsNotice = ""
		m.openModelSelector()
		return nil
	case "esc", "q":
		m.state = StateInput
		return m.input.Focus()
	}
	return nil
}

func settingsItemVisibleCount(height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return min(total, 8)
	}
	if height < 18 {
		return min(total, max(height-9, 1))
	}
	return min(total, max(height-12, 1))
}

// selectedSettingToggleAvailable is the shared source of truth for whether the
// currently highlighted row can actually be changed. Both local and global
// shortcut hints use it so a malformed/read-only/pending row never advertises
// Space as a working action.
func (m Model) selectedSettingToggleAvailable() bool {
	if m.onSettingToggle == nil || len(m.settingsPending) > 0 || len(m.settingsItems) == 0 {
		return false
	}
	selected := min(max(m.settingsCursor, 0), len(m.settingsItems)-1)
	item := m.settingsItems[selected]
	return strings.TrimSpace(item.Key) != "" && !item.Pending
}

// renderSettings draws the settings modal.
func (m Model) renderSettings() string {
	var b strings.Builder

	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := max(paletteWidth-4, 1)
	compact := m.height > 0 && m.height < 18

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	header := titleStyle.Render("Settings") + "  " + subtitleStyle.Render("/settings")
	b.WriteString(fitPanelContent(header, contentWidth))
	if compact {
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}

	// Model/provider header — shown here so the modal is the one place that
	// surfaces the whole picture; model is changed with 'm', provider via /provider.
	if m.settingsModel != "" || m.settingsProvider != "" {
		hdr := lipgloss.NewStyle().Foreground(ColorMuted)
		model := safeKeyEntryText(m.settingsModel)
		if model == "" {
			model = "not set"
		}
		provider := safeKeyEntryText(m.settingsProvider)
		if provider == "" {
			provider = "not set"
		}
		meta := truncateForWidth(fmt.Sprintf("Model: %s · Provider: %s", model, provider), max(contentWidth-2, 1))
		fmt.Fprintf(&b, "  %s\n", hdr.Render(meta))
		if !compact {
			b.WriteString("\n")
		}
	}
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true).Align(lipgloss.Center).Width(contentWidth)

	if len(m.settingsItems) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorMuted).Italic(true).Width(contentWidth)
		b.WriteString(emptyStyle.Render("No configurable settings are available for this session."))
		b.WriteString("\n")
		if notice := safeKeyEntryText(m.settingsNotice); notice != "" {
			warning := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render(notice)
			b.WriteString(fitPanelContent(warning, contentWidth))
			b.WriteString("\n")
		}
		b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, "Esc Close  ·  m Model"))
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
		category := safeKeyEntryText(item.Category)
		if category != "" && category != lastCat {
			rows = append(rows, settingRow{header: category, itemIdx: -1})
			lastCat = category
		}
		rows = append(rows, settingRow{itemIdx: i})
	}

	// Height-aware window so the modal never clips on a short terminal: show at
	// most `avail` rows, centered on the selected item. max(6, …) floors it for
	// the pre-first-WindowSizeMsg case (m.height == 0).
	avail := min(len(rows), 8)
	if m.height > 0 {
		reserved := 9
		if m.settingsNotice != "" {
			reserved++
		}
		avail = min(len(rows), max(m.height-reserved, 1))
	}
	selectedItem := min(max(m.settingsCursor, 0), len(m.settingsItems)-1)
	selRow := 0
	for ri, r := range rows {
		if r.itemIdx == selectedItem {
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
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	if start > 0 {
		marker := truncateForWidth(fmt.Sprintf("↑ %d more row(s)", start), max(contentWidth-2, 1))
		fmt.Fprintf(&b, "  %s\n", moreStyle.Render(marker))
	}
	for ri := start; ri < end; ri++ {
		r := rows[ri]
		if r.itemIdx < 0 {
			header := truncateForWidth(strings.ToUpper(r.header), max(contentWidth-2, 1))
			fmt.Fprintf(&b, "  %s\n", catStyle.Render(header))
			continue
		}
		item := m.settingsItems[r.itemIdx]
		prefix := "  "
		style := m.styles.ModalNormal
		if r.itemIdx == selectedItem {
			prefix = "> "
			style = m.styles.ModalSelected
		}
		box := "[ ]"
		if item.Pending {
			box = "[·]"
		} else if item.On {
			box = "[✓]"
		}
		name := safeKeyEntryText(item.Name)
		if name == "" {
			name = safeKeyEntryText(item.Key)
		}
		if name == "" {
			name = "Unnamed setting"
		}
		state := onOffLabel(item.On)
		if strings.TrimSpace(item.Key) == "" {
			state = "unavailable"
		} else if item.Pending {
			state = "applying…"
		} else if !item.Live {
			state += " · restart required"
		}
		label := truncateForWidth(fmt.Sprintf("%s %s · %s", box, name, state), max(contentWidth-2, 1))
		line := prefix + style.Render(label)
		fmt.Fprintf(&b, "%s\n", line)
	}
	if end < len(rows) {
		marker := truncateForWidth(fmt.Sprintf("↓ %d more row(s)", len(rows)-end), max(contentWidth-2, 1))
		fmt.Fprintf(&b, "  %s\n", moreStyle.Render(marker))
	}

	// Description of the SELECTED item only — keeps the list compact (one line
	// each) while still explaining what the cursor is on.
	if selectedItem >= 0 && selectedItem < len(m.settingsItems) {
		descStyle := m.styles.ModalMuted
		description := safeKeyEntryText(m.settingsItems[selectedItem].Desc)
		if description == "" {
			description = "No description provided."
		}
		if !compact {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "  %s\n", descStyle.Render(truncateForWidth(description, max(contentWidth-2, 1))))
	}

	if m.settingsNotice != "" {
		noticeColor := ColorSuccess
		lowerNotice := strings.ToLower(m.settingsNotice)
		if strings.Contains(lowerNotice, "applying") || strings.Contains(lowerNotice, "switching") || strings.Contains(lowerNotice, "wait") {
			noticeColor = ColorInfo
		} else if strings.Contains(lowerNotice, "unavailable") || strings.Contains(lowerNotice, "no ") || strings.Contains(lowerNotice, "couldn't") || strings.Contains(lowerNotice, "failed") {
			noticeColor = ColorWarning
		}
		notice := lipgloss.NewStyle().Foreground(noticeColor).Bold(true).Render(safeKeyEntryText(m.settingsNotice))
		b.WriteString(fitPanelContent(notice, contentWidth))
		b.WriteString("\n")
	}
	if !compact {
		b.WriteString("\n")
	}
	footer := "Esc Close · m Model · Space Toggle"
	if len(m.settingsItems) > 1 {
		footer += " · ↑/↓ Navigate"
	}
	if m.onSettingToggle == nil {
		footer = "Esc Close · m Model · Read-only"
	} else if len(m.settingsPending) > 0 {
		footer = "Esc Close · m Model · Applying…"
		if len(m.settingsItems) > 1 {
			footer += " · ↑/↓ Navigate"
		}
	} else if !m.selectedSettingToggleAvailable() {
		footer = "Esc Close · m Model · Selected unavailable"
		if len(m.settingsItems) > 1 {
			footer += " · ↑/↓ Navigate"
		}
	}
	b.WriteString(renderPromptFooterLine(footerStyle, contentWidth, footer))
	// Quick presets are the one-tap "simpler" surface — a calm dim line so modal
	// users discover them without leaving for /set.
	if !compact {
		b.WriteString("\n")
		b.WriteString(footerStyle.Render("Quick presets: /set preset safe · balanced · fast"))
		b.WriteString("\n")
	}

	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}

func onOffLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
