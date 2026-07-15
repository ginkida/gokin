package ui

import (
	"fmt"
	"strings"
	"time"

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

type settingRenderRow struct {
	header  string
	itemIdx int // -1 marks a category header
}

type settingRenderWindow struct {
	rows                 []settingRenderRow
	start, end           int
	selectedItem         int
	showAbove, showBelow bool
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
	RequestID string
	Key       string
	On        bool
	Success   bool
	Message   string
}

// SetSettingToggleCallback wires the app handler invoked when the user flips a
// setting in the modal. It receives the key and the new value; the app applies
// it live via ApplyConfig.
func (m *Model) SetSettingToggleCallback(cb func(key string, on bool)) {
	m.onSettingToggle = cb
}

// SetSettingToggleCallbackWithID wires the request-aware app handler. Each
// attempt gets a distinct opaque ID so a late result cannot settle a newer
// toggle of the same setting after the modal has been reopened.
func (m *Model) SetSettingToggleCallbackWithID(cb func(requestID, key string, on bool)) {
	m.onSettingToggleWithID = cb
}

func (m Model) settingToggleAvailable() bool {
	return m.onSettingToggleWithID != nil || m.onSettingToggle != nil
}

func (m *Model) dispatchSettingToggle(key string, on bool) {
	requestID := ""
	if m.onSettingToggleWithID != nil {
		requestID = m.nextUIRequestID("setting", &m.settingToggleRequestSeq)
	}
	if m.settingsPendingRequestIDs == nil {
		m.settingsPendingRequestIDs = make(map[string]string)
	}
	if m.settingsPendingRevisions == nil {
		m.settingsPendingRevisions = make(map[string]uint64)
	}
	m.settingsPendingRequestIDs[key] = requestID
	m.settingsPendingRevisions[key] = m.lastConfigRevision
	if m.onSettingToggleWithID != nil {
		m.onSettingToggleWithID(requestID, key, on)
		return
	}
	if m.onSettingToggle != nil {
		m.onSettingToggle(key, on)
	}
}

// configUpdateSettingSnapshot returns the settings represented by a config
// message. Settings carries the complete app snapshot; the explicit fields are
// also folded in so focused tests and older producers get the same semantics
// for the long-standing runtime toggles.
func configUpdateSettingSnapshot(msg ConfigUpdateMsg) map[string]bool {
	snapshot := make(map[string]bool, len(msg.Settings)+6)
	for key, on := range msg.Settings {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			snapshot[key] = on
		}
	}
	legacy := map[string]bool{
		"permissions":   msg.PermissionsEnabled,
		"sandbox":       msg.SandboxEnabled,
		"plan":          msg.PlanningModeEnabled,
		"compactui":     msg.CompactMode,
		"reducedmotion": msg.ReducedMotion,
		"tokens":        msg.ShowTokenUsage,
	}
	for key, on := range legacy {
		if _, present := snapshot[key]; !present {
			snapshot[key] = on
		}
	}
	return snapshot
}

// reconcileVersionedSettingSnapshot makes a committed config snapshot
// authoritative over older optimistic settings state. It also removes request
// ownership, so the result that eventually returns from that older worker is a
// harmless no-op instead of repainting stale state or feedback.
func (m *Model) reconcileVersionedSettingSnapshot(msg ConfigUpdateMsg, previousRevision uint64) {
	for key, authoritativeOn := range configUpdateSettingSnapshot(msg) {
		desiredOn, pending := m.settingsPending[key]
		baseRevision, hasBase := m.settingsPendingRevisions[key]
		if pending {
			if !hasBase {
				baseRevision = previousRevision
			}
			if msg.Revision <= baseRevision {
				continue
			}
			delete(m.settingsPending, key)
			delete(m.settingsPendingRequestIDs, key)
			delete(m.settingsPendingRevisions, key)
		}

		name := key
		found := false
		for i := range m.settingsItems {
			if m.settingsItems[i].Key != key {
				continue
			}
			m.settingsItems[i].On = authoritativeOn
			m.settingsItems[i].Pending = false
			name = m.settingsItems[i].Name
			found = true
			break
		}
		if !pending {
			continue
		}
		if name = safeKeyEntryText(name); name == "" {
			name = "Setting"
		}
		feedback := fmt.Sprintf("%s: %s", name, onOffLabel(authoritativeOn))
		if desiredOn != authoritativeOn {
			feedback = fmt.Sprintf("%s: changed elsewhere · %s", name, onOffLabel(authoritativeOn))
		}
		if m.state == StateSettings && found {
			m.settingsNotice = feedback
		} else if m.toastManager != nil {
			m.toastManager.ShowTagged("setting-"+key, ToastInfo, feedback, 4*time.Second)
		}
	}
}

// openSettings enters the settings modal with a fresh snapshot of toggles + the
// current model/provider for the header.
func (m *Model) openSettings(msg OpenSettingsMsg) {
	m.settingsReturnState = overlayReturnState(m.state)
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
	page := m.settingsPageItemCount()
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
			if !m.settingsPrimaryActionReadable() {
				return nil
			}
			if len(m.settingsPending) > 0 {
				m.settingsNotice = "Wait for the current setting to finish applying"
				return nil
			}
			item := &m.settingsItems[m.settingsCursor]
			if !m.settingToggleAvailable() {
				m.settingsNotice = "Changes are unavailable in this session"
				return nil
			}
			if item.Key == "" {
				m.settingsNotice = "This setting has no configurable key"
				return nil
			}
			item.On = !item.On // optimistic value, explicitly marked pending below
			if item.Key == "compactui" {
				// Layout changes are safe to preview immediately; the result message
				// restores the authoritative value if persistence/runtime apply fails.
				m.SetCompactMode(item.On)
			}
			item.Pending = true
			if m.settingsPending == nil {
				m.settingsPending = make(map[string]bool)
			}
			m.settingsPending[item.Key] = item.On
			m.dispatchSettingToggle(item.Key, item.On)
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
		return m.closeSettings()
	}
	return nil
}

func (m *Model) closeSettings() tea.Cmd {
	return m.restoreAsyncModal(&m.settingsReturnState)
}

func buildSettingRenderRows(items []SettingItem) []settingRenderRow {
	rows := make([]settingRenderRow, 0, len(items)+6)
	lastCategory := ""
	for i, item := range items {
		category := safeKeyEntryText(item.Category)
		if category != "" && category != lastCategory {
			rows = append(rows, settingRenderRow{header: category, itemIdx: -1})
			lastCategory = category
		}
		rows = append(rows, settingRenderRow{itemIdx: i})
	}
	return rows
}

func (m Model) settingsListRowBudget(hasMeta, hasSafety, bordered bool) int {
	if m.height <= 0 {
		// The historical no-size rendering showed eight data rows plus up to two
		// overflow markers. Preserve that useful default until WindowSizeMsg.
		return 10
	}
	compact := m.height < 18
	fixedRows := 1 // title
	if !compact {
		fixedRows++ // breathing row after title
	}
	if hasMeta {
		fixedRows++
	}
	if hasSafety {
		fixedRows++
	}
	if (hasMeta || hasSafety) && !compact {
		fixedRows++
	}
	if len(m.settingsItems) > 0 {
		fixedRows++ // selected item description
		if !compact {
			fixedRows++ // breathing row before description
		}
	}
	if m.settingsNotice != "" {
		fixedRows++
	}
	if !compact {
		fixedRows++ // breathing row before footer
	}
	fixedRows++ // footer
	if !compact {
		fixedRows += 2 // quick-presets line + trailing breathing row
	}
	if bordered {
		fixedRows += 2
	}
	return max(m.height-fixedRows, 1)
}

func chooseSettingRenderWindow(rows []settingRenderRow, selectedRow, rowBudget int) settingRenderWindow {
	window := settingRenderWindow{rows: rows}
	if len(rows) == 0 {
		return window
	}
	viewport := chooseViewportWindow(len(rows), selectedRow, rowBudget)
	window.start, window.end = viewport.start, viewport.end
	window.showAbove, window.showBelow = viewport.showAbove, viewport.showBelow
	return window
}

func (m Model) settingsRenderWindow(hasMeta, hasSafety, bordered bool) settingRenderWindow {
	rows := buildSettingRenderRows(m.settingsItems)
	selectedItem := min(max(m.settingsCursor, 0), len(m.settingsItems)-1)
	selectedRow := 0
	for i, row := range rows {
		if row.itemIdx == selectedItem {
			selectedRow = i
			break
		}
	}
	window := chooseSettingRenderWindow(rows, selectedRow, m.settingsListRowBudget(hasMeta, hasSafety, bordered))
	window.selectedItem = selectedItem
	return window
}

func (m Model) settingsPageItemCount() int {
	if len(m.settingsItems) == 0 {
		return 0
	}
	_, bordered := promptPaletteWidth(m.width)
	safetySummary, _ := settingsSafetySummary(m.settingsItems)
	hasSafety := safetySummary != ""
	window := m.settingsRenderWindow(m.settingsModel != "" || m.settingsProvider != "", hasSafety, bordered)
	count := 0
	for _, row := range window.rows[window.start:window.end] {
		if row.itemIdx >= 0 {
			count++
		}
	}
	return max(count, 1)
}

// selectedSettingToggleAvailable is the shared source of truth for whether the
// currently highlighted row can actually be changed. Both local and global
// shortcut hints use it so a malformed/read-only/pending row never advertises
// Space as a working action.
func (m Model) selectedSettingToggleAvailable() bool {
	if !m.settingToggleAvailable() || len(m.settingsPending) > 0 || len(m.settingsItems) == 0 {
		return false
	}
	selected := min(max(m.settingsCursor, 0), len(m.settingsItems)-1)
	item := m.settingsItems[selected]
	return strings.TrimSpace(item.Key) != "" && !item.Pending
}

func (m Model) settingsPrimaryActionReadable() bool {
	minHeight := 4 // selected row + description + footer + global status
	if safeKeyEntryText(m.settingsNotice) != "" {
		minHeight++
	}
	_, bordered := promptPaletteWidth(m.width)
	if bordered {
		minHeight++
	}
	return promptPrimaryActionGeometryReadable(m.width, m.height, minHeight)
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

	// Model/provider and combined safety state — shown here so the modal is the
	// one place that surfaces the whole picture. Permissions and sandbox are
	// independent toggles; their combined effect must not be inferred from two
	// distant on/off rows.
	hasMeta := m.settingsModel != "" || m.settingsProvider != ""
	if hasMeta {
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
	}
	safetySummary, safetyRisk := settingsSafetySummary(m.settingsItems)
	if safetySummary != "" {
		color := ColorMuted
		if safetyRisk == 1 {
			color = ColorWarning
		} else if safetyRisk == 2 {
			color = ColorError
		}
		safetyStyle := lipgloss.NewStyle().Foreground(color)
		line := truncateForWidth("Safety: "+safetySummary, max(contentWidth-2, 1))
		fmt.Fprintf(&b, "  %s\n", safetyStyle.Render(line))
	}
	if (hasMeta || safetySummary != "") && !compact {
		b.WriteString("\n")
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

	// Category headers, data rows and overflow markers share one exact row
	// budget. The same window drives PgUp/PgDown, so navigation cannot skip an
	// item the renderer never showed.
	window := m.settingsRenderWindow(hasMeta, safetySummary != "", bordered)
	rows, start, end := window.rows, window.start, window.end
	selectedItem := window.selectedItem

	catStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorMuted)
	moreStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	if window.showAbove {
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
	if window.showBelow {
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
	if m.selectedSettingToggleAvailable() && !m.settingsPrimaryActionReadable() {
		footer = resizeRecoveryLabel(contentWidth, "Esc Close")
	} else if !m.settingToggleAvailable() {
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
	} else {
		footer = primaryActionFooterLabel(contentWidth, footer, "Esc Close", "Space")
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

// settingsSafetySummary resolves the two independent safety toggles into the
// behavior the user will actually get. risk is 0 for guarded, 1 for a partial
// bypass, and 2 for full YOLO. Missing rows return no summary rather than
// guessing from a partial snapshot.
func settingsSafetySummary(items []SettingItem) (summary string, risk int) {
	permissionsOn, permissionsFound := false, false
	sandboxOn, sandboxFound := false, false
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Key)) {
		case "permissions":
			permissionsOn, permissionsFound = item.On, true
		case "sandbox":
			sandboxOn, sandboxFound = item.On, true
		}
	}
	if !permissionsFound || !sandboxFound {
		return "", 0
	}
	switch {
	case !permissionsOn && !sandboxOn:
		return "full YOLO · no prompts · bash unrestricted", 2
	case !permissionsOn:
		return "no prompts · bash sandboxed", 1
	case !sandboxOn:
		return "prompts on · approved bash unrestricted", 1
	default:
		return "prompts on · bash sandboxed", 0
	}
}
