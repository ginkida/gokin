package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type notificationCenterRow struct {
	toast  Toast
	active bool
}

func (m *Model) openNotificationCenter() {
	m.notificationSelected = 0
	m.notificationSelectedID = 0
	m.notificationScroll = 0
	m.notificationDetail = false
	m.notificationDetailScroll = 0
	m.notificationNotice = ""
	m.enterTransientOverlay(StateNotificationCenter)
	if rows := m.notificationRows(); len(rows) > 0 {
		m.notificationSelectedID = rows[0].toast.ID
	}
}

func (m *Model) notificationRows() []notificationCenterRow {
	if m.toastManager == nil {
		return nil
	}
	active := m.toastManager.Active()
	history := m.toastManager.History()
	rows := make([]notificationCenterRow, 0, len(active)+len(history))
	for _, toast := range active {
		rows = append(rows, notificationCenterRow{toast: toast, active: true})
	}
	for _, toast := range history {
		rows = append(rows, notificationCenterRow{toast: toast})
	}
	return rows
}

func notificationVisibleCount(height, total, noticeRows int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return min(total, 8)
	}
	return min(total, max(height-9-max(noticeRows, 0), 1))
}

// notificationPageSize is shared by rendering and selection paging. A notice
// is part of the list layout, so reserving its row here prevents the footer or
// border from being cropped after actions such as clearing notification history.
func (m Model) notificationPageSize(total int) int {
	noticeRows := 0
	if m.notificationNoticeText() != "" {
		noticeRows = 1
	}
	return notificationVisibleCount(m.height, total, noticeRows)
}

func notificationDetailVisibleCount(height, total int) int {
	if total <= 0 {
		return 0
	}
	if height <= 0 {
		return min(total, 10)
	}
	return min(total, max(height-11, 1))
}

func notificationContentWidth(termWidth int) int {
	paletteWidth, _ := promptPaletteWidth(termWidth)
	return max(paletteWidth-4, 1)
}

// A selected notification needs enough horizontal room to expose a
// distinguishable slice of its message, not just severity + an ellipsis.
// Below this boundary Enter would open details for a target the user cannot
// identify. Zero remains the pre-WindowSize/headless sentinel.
const minNotificationTargetWidth = 28

func (m Model) notificationPrimaryActionReadable() bool {
	if m.width <= 0 || m.height <= 0 {
		return true
	}
	minHeight := 4 // selected row + breathing row + local footer + global status
	rows := m.notificationRows()
	if len(rows) > 0 {
		visible := m.notificationPageSize(len(rows))
		selected := selectedNotificationIndex(rows, m.notificationSelected, m.notificationSelectedID)
		maxScroll := max(len(rows)-visible, 0)
		start := min(max(m.notificationScroll, 0), maxScroll)
		if selected < start {
			start = selected
		}
		if selected >= start+visible {
			start = selected - visible + 1
		}
		end := min(start+visible, len(rows))
		// The compositor keeps the bottom tail. Every list/disclosure row after
		// the selected target must therefore fit too, or the target itself is
		// what gets cropped while its Enter footer survives.
		minHeight += max(end-selected-1, 0)
		if end < len(rows) {
			minHeight++ // "older" disclosure below the selected page
		}
	}
	if m.notificationNoticeText() != "" {
		minHeight++
	}
	_, bordered := promptPaletteWidth(m.width)
	if bordered {
		minHeight++ // bottom border sits between the local footer and status
	}
	return m.width >= minNotificationTargetWidth && m.height >= minHeight
}

func (m Model) notificationListFooter(canNavigate, canClear bool) string {
	if !m.notificationPrimaryActionReadable() {
		return notificationResizeRecoveryFooter(notificationContentWidth(m.width))
	}
	return notificationCenterListFooter(notificationContentWidth(m.width), canNavigate, canClear)
}

func (m *Model) handleNotificationCenterKeys(msg tea.KeyMsg) tea.Cmd {
	rows := m.notificationRows()
	visible := m.notificationPageSize(len(rows))
	m.syncNotificationSelection(rows, visible)

	if m.notificationDetail {
		return m.handleNotificationDetailKeys(msg, rows)
	}

	switch msg.Type {
	case tea.KeyEscape:
		m.notificationSelected = 0
		m.notificationSelectedID = 0
		m.notificationScroll = 0
		m.notificationDetailScroll = 0
		m.notificationNotice = ""
		return m.restoreTransientOverlay()
	case tea.KeyUp:
		m.setNotificationSelection(m.notificationSelected-1, rows)
	case tea.KeyDown:
		m.setNotificationSelection(m.notificationSelected+1, rows)
	case tea.KeyPgUp:
		m.setNotificationSelection(m.notificationSelected-max(visible, 1), rows)
	case tea.KeyPgDown:
		m.setNotificationSelection(m.notificationSelected+max(visible, 1), rows)
	case tea.KeyHome:
		m.setNotificationSelection(0, rows)
	case tea.KeyEnd:
		m.setNotificationSelection(len(rows)-1, rows)
	case tea.KeyEnter, tea.KeySpace:
		if len(rows) > 0 && m.notificationPrimaryActionReadable() {
			m.notificationDetail = true
			m.notificationDetailScroll = 0
			m.notificationNotice = ""
		}
	case tea.KeyRunes:
		if len(msg.Runes) == 1 && (msg.Runes[0] == 'c' || msg.Runes[0] == 'C') {
			if m.toastManager == nil || len(m.toastManager.History()) == 0 {
				m.notificationNotice = "No earlier notifications to clear"
				return nil
			}
			if !m.notificationClearActionVisible(len(rows) > 1) {
				m.notificationNotice = "Resize to clear earlier notifications"
				return nil
			}
			m.toastManager.ClearHistory()
			m.notificationSelected = 0
			m.notificationSelectedID = 0
			m.notificationScroll = 0
			m.notificationNotice = "Earlier notifications cleared"
		}
	}
	rows = m.notificationRows()
	m.syncNotificationSelection(rows, m.notificationPageSize(len(rows)))
	return nil
}

// syncNotificationSelection keeps the logical notification selected when
// active toasts expire and move into history. The active/history partition can
// reorder rows between ticks, so an array index alone is not stable enough.
func (m *Model) syncNotificationSelection(rows []notificationCenterRow, visible int) {
	if len(rows) == 0 {
		m.notificationSelectedID = 0
		m.clampNotificationSelection(0, visible)
		return
	}
	if index := notificationIndexByID(rows, m.notificationSelectedID); index >= 0 {
		m.notificationSelected = index
	}
	m.clampNotificationSelection(len(rows), visible)
	m.notificationSelectedID = rows[m.notificationSelected].toast.ID
}

func (m *Model) setNotificationSelection(index int, rows []notificationCenterRow) {
	if len(rows) == 0 {
		m.notificationSelected = 0
		m.notificationSelectedID = 0
		return
	}
	m.notificationSelected = min(max(index, 0), len(rows)-1)
	m.notificationSelectedID = rows[m.notificationSelected].toast.ID
}

func notificationIndexByID(rows []notificationCenterRow, id int) int {
	if id == 0 {
		return -1
	}
	for index, row := range rows {
		if row.toast.ID == id {
			return index
		}
	}
	return -1
}

func selectedNotificationIndex(rows []notificationCenterRow, fallback, id int) int {
	if len(rows) == 0 {
		return 0
	}
	if index := notificationIndexByID(rows, id); index >= 0 {
		return index
	}
	return min(max(fallback, 0), len(rows)-1)
}

func (m *Model) clampNotificationSelection(total, visible int) {
	if total <= 0 {
		m.notificationSelected = 0
		m.notificationScroll = 0
		return
	}
	m.notificationSelected = min(max(m.notificationSelected, 0), total-1)
	visible = min(max(visible, 1), total)
	maxScroll := max(total-visible, 0)
	m.notificationScroll = min(max(m.notificationScroll, 0), maxScroll)
	if m.notificationSelected < m.notificationScroll {
		m.notificationScroll = m.notificationSelected
	}
	if m.notificationSelected >= m.notificationScroll+visible {
		m.notificationScroll = m.notificationSelected - visible + 1
	}
}

func (m *Model) handleNotificationDetailKeys(msg tea.KeyMsg, rows []notificationCenterRow) tea.Cmd {
	if len(rows) == 0 {
		m.notificationDetail = false
		m.notificationDetailScroll = 0
		return nil
	}
	lines := notificationDetailLines(rows[m.notificationSelected], notificationContentWidth(m.width))
	visible := notificationDetailVisibleCount(m.height, len(lines))
	maxScroll := max(len(lines)-visible, 0)
	m.notificationDetailScroll = min(max(m.notificationDetailScroll, 0), maxScroll)

	switch msg.Type {
	case tea.KeyEscape:
		m.notificationDetail = false
		m.notificationDetailScroll = 0
	case tea.KeyUp:
		m.notificationDetailScroll = max(m.notificationDetailScroll-1, 0)
	case tea.KeyDown:
		m.notificationDetailScroll = min(m.notificationDetailScroll+1, maxScroll)
	case tea.KeyPgUp:
		m.notificationDetailScroll = max(m.notificationDetailScroll-max(visible, 1), 0)
	case tea.KeyPgDown:
		m.notificationDetailScroll = min(m.notificationDetailScroll+max(visible, 1), maxScroll)
	case tea.KeyHome:
		m.notificationDetailScroll = 0
	case tea.KeyEnd:
		m.notificationDetailScroll = maxScroll
	}
	return nil
}

func (m Model) renderNotificationCenter() string {
	paletteWidth, bordered := promptPaletteWidth(m.width)
	contentWidth := notificationContentWidth(m.width)
	rows := m.notificationRows()
	if m.notificationDetail && len(rows) > 0 {
		return m.renderNotificationDetail(rows, paletteWidth, contentWidth, bordered)
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	metaStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true).Align(lipgloss.Center).Width(contentWidth)
	activeCount, historyCount := 0, 0
	if m.toastManager != nil {
		activeCount = len(m.toastManager.Active())
		historyCount = len(m.toastManager.History())
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Notifications"))
	b.WriteString("\n")
	b.WriteString(metaStyle.Render(fmt.Sprintf("%d active · %d earlier", activeCount, historyCount)))
	b.WriteString("\n\n")

	if len(rows) == 0 {
		b.WriteString(dimStyle.Render("No notifications yet."))
		b.WriteString("\n")
		b.WriteString(metaStyle.Render("Errors, warnings, and status updates will appear here."))
		b.WriteString("\n")
	} else {
		visible := m.notificationPageSize(len(rows))
		selected := selectedNotificationIndex(rows, m.notificationSelected, m.notificationSelectedID)
		maxScroll := max(len(rows)-visible, 0)
		start := min(max(m.notificationScroll, 0), maxScroll)
		if selected < start {
			start = selected
		}
		if selected >= start+visible {
			start = selected - visible + 1
		}
		end := min(start+visible, len(rows))
		if start > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("↑ %d newer", start)))
			b.WriteString("\n")
		}
		for i, row := range rows[start:end] {
			b.WriteString(renderNotificationCenterRow(row, contentWidth, start+i == selected))
			b.WriteString("\n")
		}
		if end < len(rows) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("↓ %d older", len(rows)-end)))
			b.WriteString("\n")
		}
	}

	if notice := m.notificationNoticeText(); notice != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorInfo).Bold(true).Render(notice))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	footer := m.notificationListFooter(len(rows) > 1, historyCount > 0)
	if len(rows) == 0 {
		footer = "Esc Back"
	}
	b.WriteString(footerStyle.Render(footer))
	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}

// notificationNoticeText suppresses notices whose premise is no longer true.
// Toasts can expire into history and the terminal can be resized while this
// overlay is open, so persisting the last key-handler string verbatim can claim
// that there is nothing to clear—or that resizing is still required—after the
// state has already changed.
func (m Model) notificationNoticeText() string {
	notice := safeKeyEntryText(m.notificationNotice)
	if notice == "" {
		return ""
	}
	historyCount := 0
	if m.toastManager != nil {
		historyCount = len(m.toastManager.History())
	}
	switch notice {
	case "No earlier notifications to clear", "Earlier notifications cleared":
		if historyCount > 0 {
			return ""
		}
	case "Resize to clear earlier notifications":
		if historyCount == 0 {
			return ""
		}
		// Probe the current geometry without the obsolete notice consuming a
		// layout row. This value copy is read-only and avoids a resize handler
		// dependency in tui.go.
		probe := m
		probe.notificationNotice = ""
		if probe.notificationClearActionVisible(len(probe.notificationRows()) > 1) {
			return ""
		}
	}
	return notice
}

func renderNotificationCenterRow(row notificationCenterRow, width int, selected bool) string {
	label, color := notificationSeverity(row.toast.Type)
	message := safeKeyEntryText(row.toast.Message)
	if title := safeKeyEntryText(row.toast.Title); title != "" {
		if message != "" {
			message = title + ": " + message
		} else {
			message = title
		}
	}
	if message == "" {
		message = "Notification"
	}
	when := row.toast.CreatedAt.Format("15:04")
	if row.active {
		when = "now"
	}
	cursor := "  "
	if selected {
		cursor = lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true).Render("› ")
	}
	prefix := cursor + lipgloss.NewStyle().Foreground(color).Bold(true).Render(label)
	suffix := lipgloss.NewStyle().Foreground(ColorDim).Render(" · " + when)
	messageBudget := max(width-lipgloss.Width(prefix)-lipgloss.Width(suffix)-3, 1)
	line := prefix + " · " + truncateForWidth(message, messageBudget) + suffix
	return fitPanelContent(line, width)
}

func (m Model) renderNotificationDetail(rows []notificationCenterRow, paletteWidth, contentWidth int, bordered bool) string {
	selected := selectedNotificationIndex(rows, m.notificationSelected, m.notificationSelectedID)
	row := rows[selected]
	lines := notificationDetailLines(row, contentWidth)
	visible := notificationDetailVisibleCount(m.height, len(lines))
	maxScroll := max(len(lines)-visible, 0)
	start := min(max(m.notificationDetailScroll, 0), maxScroll)
	end := min(start+visible, len(lines))

	label, color := notificationSeverity(row.toast.Type)
	when := row.toast.CreatedAt.Format("15:04")
	status := "earlier"
	if row.active {
		when, status = "now", "active"
	}
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
	footerStyle := dimStyle.Align(lipgloss.Center).Width(contentWidth)

	var b strings.Builder
	b.WriteString(fitPanelContent(titleStyle.Render("Notification details"), contentWidth))
	b.WriteString("\n")
	meta := fmt.Sprintf("%s · %s · %s · %d of %d", label, status, when, selected+1, len(rows))
	b.WriteString(fitPanelContent(lipgloss.NewStyle().Foreground(color).Bold(true).Render(meta), contentWidth))
	b.WriteString("\n\n")
	if start > 0 {
		b.WriteString(dimStyle.Render(fmt.Sprintf("↑ %d lines above", start)))
		b.WriteString("\n")
	}
	for _, line := range lines[start:end] {
		b.WriteString(fitPanelContent(line, contentWidth))
		b.WriteString("\n")
	}
	if end < len(lines) {
		b.WriteString(dimStyle.Render(fmt.Sprintf("↓ %d lines below", len(lines)-end)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(notificationDetailFooter(contentWidth, maxScroll > 0)))
	return wrapPromptContainer(b.String(), paletteWidth, bordered, ColorPrimary)
}

func notificationCenterListFooter(width int, canNavigate, canClear bool) string {
	if !canNavigate {
		candidates := []string{
			"Enter Details · Esc Back",
			"Enter Details · Esc",
			"Esc · Enter Details",
			"Esc · Enter",
		}
		if canClear {
			candidates = append([]string{
				"Enter Details · c Clear earlier · Esc Back",
				"Enter Details · c Clear · Esc",
			}, candidates...)
		}
		return firstFooterThatFits(candidates, width)
	}
	candidates := []string{
		"↑/↓ Select · Enter Details · Esc Back",
		"↑↓ Select · Enter Details · Esc Back",
		"↑↓ · Enter Details · Esc Back",
		"Esc · Enter Details",
		"Esc · Enter",
	}
	if canClear {
		candidates = append([]string{
			"↑/↓ Select · Enter Details · c Clear earlier · Esc Back",
			"↑↓ Select · Enter Details · c Clear · Esc Back",
			"↑↓ · Enter Details · c Clear · Esc Back",
		}, candidates...)
	}
	return firstFooterThatFits(candidates, width)
}

func (m Model) notificationClearActionVisible(canNavigate bool) bool {
	// Zero dimensions are the pre-WindowSize sentinel used by headless tests and
	// embedders. Do not invent a tiny terminal and disable their existing input.
	if m.width <= 0 || m.height <= 0 {
		return true
	}
	// A one-row frame contains only the global status bar, so no notification
	// footer action can actually be visible regardless of width.
	if m.height < 2 {
		return false
	}
	footer := m.notificationListFooter(canNavigate, true)
	return strings.Contains(footer, "Clear")
}

func notificationResizeRecoveryFooter(width int) string {
	return firstFooterThatFits([]string{"Resize · Esc Back", "Resize · Esc", "↔ · Esc", "Esc"}, width)
}

func notificationDetailFooter(width int, canScroll bool) string {
	candidates := []string{"Esc Back to list", "Esc Back", "Esc"}
	if canScroll {
		candidates = []string{
			"↑/↓ Scroll · PgUp/PgDn Page · Esc Back to list",
			"↑↓ Scroll · PgUp/PgDn · Esc Back to list",
			"↑↓ Scroll · Esc List",
			"Esc · ↑↓",
		}
	}
	return firstFooterThatFits(candidates, width)
}

func firstFooterThatFits(candidates []string, width int) string {
	for _, candidate := range candidates {
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return truncateForWidth(candidates[len(candidates)-1], max(width, 1))
}

func notificationDetailLines(row notificationCenterRow, width int) []string {
	width = max(width, 1)
	title := safeKeyEntryText(row.toast.Title)
	message := safeKeyEntryText(row.toast.Message)
	if title == "" && message == "" {
		message = "Notification"
	}
	var lines []string
	if title != "" {
		titleStyle := lipgloss.NewStyle().Bold(true)
		for _, line := range strings.Split(wrapText(title, width), "\n") {
			lines = append(lines, titleStyle.Render(strings.TrimRight(line, " ")))
		}
	}
	if message != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		for _, line := range strings.Split(wrapText(message, width), "\n") {
			lines = append(lines, strings.TrimRight(line, " "))
		}
	}
	return lines
}

func notificationSeverity(toastType ToastType) (string, lipgloss.Color) {
	switch toastType {
	case ToastError:
		return "ERROR", ColorError
	case ToastWarning:
		return "WARN", ColorWarning
	case ToastSuccess:
		return "DONE", ColorSuccess
	default:
		return "INFO", ColorInfo
	}
}
