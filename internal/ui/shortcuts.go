package ui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ShortcutsOverlay model for displaying keyboard shortcuts.
type ShortcutsOverlay struct {
	visible     bool
	styles      *Styles
	categories  []ShortcutCategory
	scrollIndex int
	searchQuery string
	filtered    []ShortcutCategory
	pageSize    int
}

// ShortcutCategory represents a category of shortcuts.
type ShortcutCategory struct {
	Name      string
	Shortcuts []Shortcut
}

// Shortcut represents a single keyboard shortcut.
type Shortcut struct {
	Keys        []string
	Description string
}

// DefaultShortcuts returns the default keyboard shortcuts.
func DefaultShortcuts() []ShortcutCategory {
	return []ShortcutCategory{
		{
			Name: "Navigation",
			Shortcuts: []Shortcut{
				{Keys: []string{"↑", "↓"}, Description: "Navigate suggestions or history"},
				{Keys: []string{"PgUp", "PgDn"}, Description: "Scroll output by page"},
				{Keys: []string{"Ctrl", "b"}, Description: "Scroll up"},
				{Keys: []string{"Ctrl", "f"}, Description: "Scroll down"},
				{Keys: []string{"Ctrl", "u"}, Description: "Clear input, or scroll half-page up when input is empty"},
				{Keys: []string{"Ctrl", "d"}, Description: "Scroll half-page down (when input is empty)"},
			},
		},
		{
			Name: "Input",
			Shortcuts: []Shortcut{
				{Keys: []string{"Enter"}, Description: "Send message"},
				{Keys: []string{"?"}, Description: "Open searchable keyboard shortcuts"},
				{Keys: []string{"Alt", "Enter"}, Description: "Insert newline (multi-line)"},
				{Keys: []string{"Ctrl", "J"}, Description: "Insert newline (works in every terminal)"},
				{Keys: []string{"Tab"}, Description: "Accept ghost text / autocomplete / select suggestion"},
				{Keys: []string{"Ctrl", "R"}, Description: "Search input history"},
				{Keys: []string{"Esc"}, Description: "Interrupt request / close modal"},
				{Keys: []string{"Ctrl", "C"}, Description: "Interrupt active request; press twice consecutively to quit when idle"},
			},
		},
		{
			Name: "Command Center",
			Shortcuts: []Shortcut{
				{Keys: []string{"Ctrl", "p"}, Description: "Command Palette (All Actions)"},
				{Keys: []string{"Ctrl", "S"}, Description: "Open settings (toggles)"},
				{Keys: []string{"Alt", "N"}, Description: "Open notification history"},
				{Keys: []string{"Ctrl", "K"}, Description: "Open model selector"},
				{Keys: []string{"Ctrl", "E"}, Description: "Toggle last output (expand appends; compact keeps existing scrollback)"},
				{Keys: []string{"E"}, Description: "Set expanded/compact default for new tool outputs"},
				{Keys: []string{"Ctrl", "H"}, Description: "Context Observatory (Technical Health)"},
				{Keys: []string{"Ctrl", "G"}, Description: "Toggle select mode (freeze + native selection)"},
				{Keys: []string{"Ctrl", "O"}, Description: "Live activity detail on/off (works while streaming)"},
				{Keys: []string{"Ctrl", "A"}, Description: "Toggle agent tree panel"},
				{Keys: []string{"Ctrl", "T"}, Description: "Toggle task list panel"},
				{Keys: []string{"Ctrl", "X"}, Description: "Expand / collapse plan progress panel"},
				{Keys: []string{"Ctrl", "L"}, Description: "Clear the output screen"},
				{Keys: []string{"Alt", "C"}, Description: "Copy last AI response"},
			},
		},
		{
			Name: "Session Mode (Claude Code-style cycle)",
			Shortcuts: []Shortcut{
				{Keys: []string{"Shift", "Tab"}, Description: "Cycle Normal → Plan → YOLO → Normal"},
				// Annotated next three entries to describe what each stop
				// of the cycle does. Not actual shortcuts themselves — they
				// render as dim rows under the binding for discoverability.
				{Keys: []string{"·"}, Description: "Normal: agent asks before write/edit/bash"},
				{Keys: []string{"·"}, Description: "Plan: read-only exploration, proposes plan for approval"},
				{Keys: []string{"·"}, Description: "YOLO: permissions OFF, sandbox OFF, auto-approve everything"},
			},
		},
		{
			Name: "Diff Preview",
			Shortcuts: []Shortcut{
				{Keys: []string{"y"}, Description: "Apply this diff"},
				{Keys: []string{"n", "Esc"}, Description: "Reject this diff"},
				{Keys: []string{"A"}, Description: "Apply all remaining diffs"},
				{Keys: []string{"R"}, Description: "Reject all remaining diffs"},
				{Keys: []string{"Tab"}, Description: "Switch focus (multi-file only)"},
				{Keys: []string{"[", "]"}, Description: "Previous/next change (single-file only)"},
				{Keys: []string{"j", "k"}, Description: "Scroll diff, or move through file list"},
			},
		},
		{
			Name: "History",
			Shortcuts: []Shortcut{
				{Keys: []string{"/undo"}, Description: "Undo last change"},
				{Keys: []string{"/redo"}, Description: "Redo undone change"},
			},
		},
		{
			Name: "Session Management",
			Shortcuts: []Shortcut{
				{Keys: []string{"/clear"}, Description: "Clear conversation"},
				{Keys: []string{"/save"}, Description: "Save session"},
				{Keys: []string{"/resume"}, Description: "Resume session"},
				{Keys: []string{"/cost"}, Description: "Show token usage"},
			},
		},
	}
}

// NewShortcutsOverlay creates a new shortcuts overlay.
func NewShortcutsOverlay(styles *Styles) *ShortcutsOverlay {
	return &ShortcutsOverlay{
		visible:     false,
		styles:      styles,
		categories:  DefaultShortcuts(),
		scrollIndex: 0,
		searchQuery: "",
		filtered:    nil, // nil means show all
	}
}

// Show displays the shortcuts overlay.
func (m *ShortcutsOverlay) Show() {
	m.visible = true
	m.scrollIndex = 0
	m.searchQuery = ""
	m.pageSize = 0
}

// Hide hides the shortcuts overlay.
func (m *ShortcutsOverlay) Hide() {
	m.visible = false
}

// IsVisible returns whether the overlay is visible.
func (m *ShortcutsOverlay) IsVisible() bool {
	return m.visible
}

// Toggle toggles the visibility of the overlay.
func (m *ShortcutsOverlay) Toggle() {
	m.visible = !m.visible
	if m.visible {
		m.scrollIndex = 0
		m.searchQuery = ""
		m.pageSize = 0
	}
}

// ScrollUp scrolls the shortcuts list up.
func (m *ShortcutsOverlay) ScrollUp() {
	if m.scrollIndex > 0 {
		m.scrollIndex--
	}
}

// ScrollDown scrolls the shortcuts list down.
func (m *ShortcutsOverlay) ScrollDown() {
	maxScroll := len(flattenShortcuts(m.getFilteredCategories())) - 1
	if m.scrollIndex < maxScroll {
		m.scrollIndex++
	}
}

// PageUp and PageDown move by the number of shortcut rows rendered on the
// previous frame. A small fallback keeps keyboard navigation useful before
// the first View call.
func (m *ShortcutsOverlay) PageUp() {
	m.scrollBy(-max(m.pageSize, 5))
}

func (m *ShortcutsOverlay) PageDown() {
	m.scrollBy(max(m.pageSize, 5))
}

func (m *ShortcutsOverlay) ScrollToStart() {
	m.scrollIndex = 0
}

func (m *ShortcutsOverlay) ScrollToEnd() {
	m.scrollIndex = max(len(flattenShortcuts(m.getFilteredCategories()))-1, 0)
}

func (m *ShortcutsOverlay) scrollBy(delta int) {
	maxScroll := max(len(flattenShortcuts(m.getFilteredCategories()))-1, 0)
	m.scrollIndex = min(max(m.scrollIndex+delta, 0), maxScroll)
}

// SetSearch sets the search query for filtering shortcuts.
func (m *ShortcutsOverlay) SetSearch(query string) {
	query = ansi.Strip(query)
	m.searchQuery = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, query)
	m.scrollIndex = 0 // Reset scroll when searching
	m.pageSize = 0
}

// ClearSearch clears the search query.
func (m *ShortcutsOverlay) ClearSearch() {
	m.searchQuery = ""
	m.scrollIndex = 0
	m.pageSize = 0
}

// BackspaceSearch removes one user-perceived character. Emoji modifiers,
// combining marks and ZWJ sequences must disappear as a unit instead of
// leaving a visually different fragment behind.
func (m *ShortcutsOverlay) BackspaceSearch() {
	m.SetSearch(removeLastGrapheme(m.searchQuery))
}

// GetSearch returns the current search query.
func (m *ShortcutsOverlay) GetSearch() string {
	return m.searchQuery
}

// getFilteredCategories returns categories filtered by search query.
func (m *ShortcutsOverlay) getFilteredCategories() []ShortcutCategory {
	if m.searchQuery == "" {
		return m.categories
	}

	query := strings.ToLower(m.searchQuery)
	var filtered []ShortcutCategory

	for _, cat := range m.categories {
		var matchingShortcuts []Shortcut

		// Check if category name matches
		categoryMatches := strings.Contains(strings.ToLower(cat.Name), query)

		// Filter shortcuts within category
		for _, shortcut := range cat.Shortcuts {
			// Check if description matches
			descMatches := strings.Contains(strings.ToLower(shortcut.Description), query)

			// Check if any key matches
			keyMatches := false
			for _, key := range shortcut.Keys {
				if strings.Contains(strings.ToLower(key), query) {
					keyMatches = true
					break
				}
			}

			if categoryMatches || descMatches || keyMatches {
				matchingShortcuts = append(matchingShortcuts, shortcut)
			}
		}

		// Add category if it has matching shortcuts
		if len(matchingShortcuts) > 0 {
			filtered = append(filtered, ShortcutCategory{
				Name:      cat.Name,
				Shortcuts: matchingShortcuts,
			})
		}
	}

	return filtered
}

type shortcutEntry struct {
	category string
	shortcut Shortcut
}

func flattenShortcuts(categories []ShortcutCategory) []shortcutEntry {
	var entries []shortcutEntry
	for _, category := range categories {
		for _, shortcut := range category.Shortcuts {
			entries = append(entries, shortcutEntry{category: category.Name, shortcut: shortcut})
		}
	}
	return entries
}

func shortcutsFooterLabel(width int, full, escape string) string {
	width = max(width, 1)
	for _, candidate := range []string{full, escape, "Esc", "↔"} {
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return "↔"
}

// View renders the shortcuts overlay. Dimensions returned by the helpers are
// outer dimensions: border and padding are subtracted before content sizing so
// the overlay never relies on the parent compositor to crop it.
func (m *ShortcutsOverlay) View(width, height int) string {
	if !m.visible {
		return ""
	}

	outerWidth := shortcutsOverlayWidth(width)
	outerHeight := shortcutsOverlayHeight(height)
	// Rounded borders require at least one content cell in both dimensions.
	// Degenerate panes still need a truthful recovery row, so drop the border
	// instead of letting its two chrome cells overflow the terminal.
	bordered := outerWidth >= 3 && outerHeight >= 3
	borderCells := 0
	if bordered {
		borderCells = 2
	}
	horizontalPadding := 2
	if outerWidth < 12 {
		horizontalPadding = 0
	}
	verticalPadding := 1
	if outerHeight < 10 {
		verticalPadding = 0
	}
	innerWidth := max(outerWidth-borderCells-horizontalPadding*2, 1)
	innerHeight := max(outerHeight-borderCells-verticalPadding*2, 1)

	containerStyle := lipgloss.NewStyle().
		Width(max(outerWidth-borderCells, 1)).
		Height(max(outerHeight-borderCells, 1)).
		Background(ColorBg).
		Padding(verticalPadding, horizontalPadding)
	if bordered {
		containerStyle = containerStyle.
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorSecondary)
	}
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorSecondary)
	searchStyle := lipgloss.NewStyle().Foreground(ColorAccent).Italic(true)
	categoryStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	footerStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	title := titleStyle.Render(truncateForWidth("Keyboard Shortcuts", innerWidth))
	searchLabel := "Type to filter shortcuts..."
	if m.searchQuery != "" {
		prefix := fmt.Sprintf("%s Filter: ", MessageIcons["info"])
		queryWidth := max(innerWidth-lipgloss.Width(prefix), 1)
		searchLabel = prefix + truncateTailForWidth(m.searchQuery+"_", queryWidth)
	}
	search := searchStyle.Render(truncateForWidth(searchLabel, innerWidth))

	entries := flattenShortcuts(m.getFilteredCategories())
	if len(entries) == 0 {
		m.pageSize = 0
		status := truncateForWidth("No matching shortcuts found.", innerWidth)
		footer := footerStyle.Render(shortcutsFooterLabel(innerWidth, "Esc clear  ·  Keep typing", "Esc clear"))
		var rows []string
		switch {
		case innerHeight >= 6:
			rows = []string{title, search, "", status, "", footer}
		case innerHeight == 5:
			rows = []string{title, search, "", status, footer}
		case innerHeight == 4:
			rows = []string{title, search, status, footer}
		case innerHeight == 3:
			rows = []string{title, status, footer}
		case innerHeight == 2:
			rows = []string{status, footer}
		default:
			rows = []string{footer}
		}
		return containerStyle.Render(strings.Join(rows, "\n"))
	}

	m.scrollIndex = min(max(m.scrollIndex, 0), len(entries)-1)
	// Footer + at least one shortcut are the non-negotiable compact layout.
	// Add title/search/separators only when they leave room for actual content.
	rows := make([]string, 0, innerHeight)
	itemBudget := max(innerHeight-1, 0) // reserve footer
	switch {
	case itemBudget >= 3:
		rows = append(rows, title, search)
		itemBudget -= 2
	case itemBudget >= 2:
		rows = append(rows, title)
		itemBudget--
	}
	if len(rows) > 0 && itemBudget >= 3 {
		rows = append(rows, "")
		itemBudget--
	}
	footerSeparator := itemBudget >= 4
	if footerSeparator {
		itemBudget--
	}
	lastCategory := ""
	end := m.scrollIndex
	used := 0
	for end < len(entries) {
		entry := entries[end]
		needsHeader := entry.category != lastCategory
		needed := 1
		if needsHeader {
			needed++
		}
		if used+needed > itemBudget {
			break
		}
		if needsHeader {
			rows = append(rows, categoryStyle.Render(truncateForWidth("● "+entry.category, innerWidth)))
			lastCategory = entry.category
			used++
		}
		rows = append(rows, renderShortcutEntry(entry.shortcut, innerWidth))
		used++
		end++
	}
	if end == m.scrollIndex && m.scrollIndex < len(entries) {
		if itemBudget > 0 {
			rows = append(rows, renderShortcutEntry(entries[m.scrollIndex].shortcut, innerWidth))
			end++
		}
	}
	renderedItems := end - m.scrollIndex
	m.pageSize = renderedItems

	closeHint := "Esc close"
	filterHint := "Type to filter"
	if m.searchQuery != "" {
		closeHint = "Esc clear"
		filterHint = "Keep typing"
	}
	footer := closeHint + "  ·  " + filterHint
	if renderedItems == 0 {
		footer = resizeRecoveryLabel(innerWidth, closeHint)
	} else if m.scrollIndex > 0 || end < len(entries) {
		footer = fmt.Sprintf("%s  ·  ↑/↓ %d–%d/%d  ·  PgUp/PgDn", closeHint, m.scrollIndex+1, end, len(entries))
	}
	footer = shortcutsFooterLabel(innerWidth, footer, closeHint)
	if footerSeparator {
		rows = append(rows, "")
	}
	rows = append(rows, footerStyle.Render(footer))
	return containerStyle.Render(strings.Join(rows, "\n"))
}

func renderShortcutEntry(shortcut Shortcut, width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(ColorText).Background(ColorBorder).Bold(true).Padding(0, 1)
	descStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	keys := strings.Join(shortcut.Keys, " + ")
	if width < 3 {
		// Horizontal keycap padding alone would exceed a one/two-cell pane.
		return lipgloss.NewStyle().Foreground(ColorText).Bold(true).Render(truncateForWidth(keys, max(width, 1)))
	}
	keyPart := keyStyle.Render(keys)
	remaining := width - lipgloss.Width(keyPart) - 1
	if remaining <= 0 {
		return keyStyle.Render(truncateForWidth(keys, max(width-2, 1)))
	}
	return keyPart + " " + descStyle.Render(truncateForWidth(shortcut.Description, remaining))
}

func shortcutsOverlayWidth(width int) int {
	switch {
	case width <= 0:
		return 80
	case width <= 8:
		return max(width, 1)
	case width < 84:
		return width - 4
	default:
		return 80
	}
}

func shortcutsOverlayHeight(height int) int {
	switch {
	case height <= 0:
		return 25
	case height <= 8:
		return max(height, 1)
	case height < 29:
		return height - 4
	default:
		return 25
	}
}

// ContextualHelp provides contextual help based on current UI state.
type ContextualHelp struct {
	styles   *Styles
	hints    []string
	position int // 0 = top, 1 = bottom
}

// NewContextualHelp creates a new contextual help.
func NewContextualHelp(styles *Styles, position int) *ContextualHelp {
	return &ContextualHelp{
		styles:   styles,
		hints:    []string{},
		position: position,
	}
}

// SetHints sets the help hints.
func (h *ContextualHelp) SetHints(hints []string) {
	h.hints = hints
}

// AddHint adds a single hint.
func (h *ContextualHelp) AddHint(hint string) {
	h.hints = append(h.hints, hint)
}

// Clear clears all hints.
func (h *ContextualHelp) Clear() {
	h.hints = []string{}
}

// View renders the contextual help.
func (h *ContextualHelp) View(width int) string {
	if len(h.hints) == 0 {
		return ""
	}

	helpStyle := lipgloss.NewStyle().
		Foreground(ColorDim)

	// Join hints with separator
	hintText := strings.Join(h.hints, " • ")

	// Truncate if too long (truncateForWidth floors at 0 / handles width<=3,
	// so a narrow terminal can't drive maxWidth-3 negative → panic).
	hintText = truncateForWidth(hintText, width-4)

	return helpStyle.Render(hintText)
}

// QuickAction represents a quick action button.
type QuickAction struct {
	Label    string
	Shortcut string
	Action   func()
}

// QuickActionsBar displays quick action buttons.
type QuickActionsBar struct {
	actions []QuickAction
	styles  *Styles
	visible bool
}

// NewQuickActionsBar creates a new quick actions bar.
func NewQuickActionsBar(styles *Styles) *QuickActionsBar {
	return &QuickActionsBar{
		actions: []QuickAction{},
		styles:  styles,
		visible: true,
	}
}

// SetActions sets the available actions.
func (b *QuickActionsBar) SetActions(actions []QuickAction) {
	b.actions = actions
}

// Show shows the bar.
func (b *QuickActionsBar) Show() {
	b.visible = true
}

// Hide hides the bar.
func (b *QuickActionsBar) Hide() {
	b.visible = false
}

// View renders the quick actions bar.
func (b *QuickActionsBar) View(width int) string {
	if !b.visible || len(b.actions) == 0 {
		return ""
	}

	containerStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		MarginTop(1).
		MarginBottom(1)

	var content strings.Builder

	for i, action := range b.actions {
		if i > 0 {
			content.WriteString("  ")
		}

		keyStyle := lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorBorder).
			Bold(true).
			Padding(0, 1)

		labelStyle := lipgloss.NewStyle().
			Foreground(ColorMuted)

		content.WriteString(keyStyle.Render(action.Shortcut))
		content.WriteString(" ")
		content.WriteString(labelStyle.Render(action.Label))
	}

	return containerStyle.Render(content.String())
}
