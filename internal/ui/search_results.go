package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// SearchAction represents user actions on search results.
type SearchAction int

const (
	SearchActionNone SearchAction = iota
	SearchActionOpen
	SearchActionEdit
	SearchActionCopyPath
	SearchActionClose
)

// SearchResult represents a single search result.
type SearchResult struct {
	FilePath   string
	LineNumber int
	Content    string
	Context    []string // Lines before/after for context
	MatchCount int      // Number of matches in this file
}

// SearchResultsRequestMsg is sent to display search results.
type SearchResultsRequestMsg struct {
	Query   string
	Results []SearchResult
	Tool    string // "grep", "glob", etc.
}

// SearchResultsActionMsg is sent when user performs an action.
type SearchResultsActionMsg struct {
	Action     SearchAction
	FilePath   string
	LineNumber int
	triggerKey string
	// ownerGeneration identifies the particular overlay opening that emitted
	// this asynchronous command. It prevents a delayed action from an already
	// closed search surface from mutating a later search surface.
	ownerGeneration uint64
}

// SearchResultsModel is the UI for interactive search results.
type SearchResultsModel struct {
	results         []SearchResult
	selectedIndex   int
	viewport        viewport.Model
	previewPane     viewport.Model
	query           string
	tool            string
	styles          *Styles
	width           int
	height          int
	showPreview     bool
	actionsLinked   bool
	actionsKnown    bool
	ownerGeneration uint64
	actionPending   bool

	// Callback for actions
	onAction func(action SearchAction, filePath string, lineNum int)
}

func (m *SearchResultsModel) setOwnerGeneration(generation uint64) {
	m.ownerGeneration = generation
}

const minSearchResultsSplitWidth = 64

// Tiny full-screen workspaces keep the status row and tail-crop the body. At
// these dimensions the action footer may survive while the selected target is
// gone, so target-dependent actions must fail closed until both axes are
// readable. A zero dimension means Bubble Tea has not announced geometry yet;
// preserve the historical headless/standalone behavior in that sentinel state.
func workspaceTargetActionsUnreadable(width, height, minTargetWidth int) bool {
	return width > 0 && height > 0 && (width < minTargetWidth || height <= 4)
}

const minSearchTargetWidth = 7 // two-cell cursor + at least one path cell inside the viewport

func tinyWorkspaceResizeRecoveryHint(width int) string {
	switch {
	case width >= 20:
		return "Esc/q Close · Resize"
	case width >= 10:
		return "q · Resize"
	case width >= 5:
		return "q · ↔"
	case width >= 3:
		return "q ↔"
	default:
		return "q"
	}
}

// NewSearchResultsModel creates a new search results model.
func NewSearchResultsModel(styles *Styles) SearchResultsModel {
	vp := viewport.New(40, 15)
	vp.MouseWheelEnabled = true

	preview := viewport.New(40, 15)
	preview.MouseWheelEnabled = true

	return SearchResultsModel{
		viewport:    vp,
		previewPane: preview,
		styles:      styles,
		showPreview: false,
	}
}

// SetSize sets the size of the search results view.
func (m *SearchResultsModel) SetSize(width, height int) {
	m.width = max(width, 0)
	m.height = max(height, 0)
	contentHeight := max(m.height-9, 1)

	if m.previewVisible() && m.width >= minSearchResultsSplitWidth {
		resultsOuterWidth := (m.width - 1) / 2
		previewOuterWidth := m.width - resultsOuterWidth - 1
		m.viewport.Width = max(resultsOuterWidth-4, 1)
		m.previewPane.Width = max(previewOuterWidth-4, 1)
	} else {
		panelWidth := max(m.width-4, 1)
		m.viewport.Width = panelWidth
		m.previewPane.Width = panelWidth
	}
	m.viewport.Height = contentHeight
	m.previewPane.Height = contentHeight
	m.ensureSelectionVisible()
}

func (m SearchResultsModel) renderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return max(m.viewport.Width+4, minSearchResultsSplitWidth)
}

// SetResults sets the search results to display.
func (m *SearchResultsModel) SetResults(query, tool string, results []SearchResult) {
	m.actionPending = false
	m.query = safeKeyEntryText(query)
	m.tool = safeKeyEntryText(tool)
	m.results = make([]SearchResult, len(results))
	for i := range results {
		m.results[i] = results[i]
		m.results[i].Context = append([]string(nil), results[i].Context...)
	}
	m.selectedIndex = 0
	m.viewport.GotoTop()
	m.updateViewport()
	m.updatePreview()
	if m.width > 0 && m.height > 0 {
		m.SetSize(m.width, m.height)
	}
}

// SetActionCallback sets the callback for user actions.
func (m *SearchResultsModel) SetActionCallback(callback func(SearchAction, string, int)) {
	m.onAction = callback
}

// SetActionsLinked tells the reusable panel whether its parent can carry out
// Open/Edit messages. Preview and path copying remain local UI actions.
func (m *SearchResultsModel) SetActionsLinked(linked bool) {
	m.actionsLinked = linked
	m.actionsKnown = true
}

func (m SearchResultsModel) actionsAvailable() bool {
	// Standalone Bubble Tea users consume the emitted ActionMsg directly and
	// need no callback. Only a parent that explicitly owns dispatch can declare
	// that no downstream handler is linked.
	return !m.actionsKnown || m.actionsLinked || m.onAction != nil
}

func (m SearchResultsModel) canNavigateResults() bool {
	return len(m.results) > 1
}

func (m SearchResultsModel) canPageResults() bool {
	return m.canNavigateResults() && len(m.results) > max(m.viewport.Height, 1)
}

func (m SearchResultsModel) canScrollPreview() bool {
	return m.previewVisible() && m.previewPane.TotalLineCount() > m.previewPane.Height
}

// updateViewport updates the viewport content based on current selection.
func (m *SearchResultsModel) updateViewport() {
	var content strings.Builder

	for i, result := range m.results {
		line := m.formatResultLine(i, result)
		content.WriteString(line)
		content.WriteString("\n")
	}
	if len(m.results) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		content.WriteString(emptyStyle.Render(m.emptyStateText()))
		content.WriteString("\n")
	}

	m.viewport.SetContent(content.String())
	m.ensureSelectionVisible()
}

func (m *SearchResultsModel) emptyStateText() string {
	query := strings.TrimSpace(m.query)
	if query == "" {
		return "No search results to display"
	}
	switch strings.ToLower(strings.TrimSpace(m.tool)) {
	case "glob":
		return fmt.Sprintf("No files match %q\nTry a less specific pattern", query)
	case "grep", "search":
		return fmt.Sprintf("No matches for %q\nTry a broader pattern or check the search path", query)
	default:
		return fmt.Sprintf("No results for %q\nTry a broader query", query)
	}
}

func (m *SearchResultsModel) previewVisible() bool {
	return m.showPreview && len(m.results) > 0
}

func (m *SearchResultsModel) ensureSelectionVisible() {
	if len(m.results) == 0 || m.viewport.Height <= 0 {
		m.viewport.SetYOffset(0)
		return
	}
	if m.selectedIndex < 0 {
		m.selectedIndex = 0
	}
	if m.selectedIndex >= len(m.results) {
		m.selectedIndex = len(m.results) - 1
	}
	if m.selectedIndex < m.viewport.YOffset {
		m.viewport.SetYOffset(m.selectedIndex)
	} else if m.selectedIndex >= m.viewport.YOffset+m.viewport.Height {
		m.viewport.SetYOffset(m.selectedIndex - m.viewport.Height + 1)
	}
}

func (m *SearchResultsModel) selectIndex(index int) {
	if len(m.results) == 0 {
		m.selectedIndex = 0
		m.updateViewport()
		m.updatePreview()
		return
	}
	m.selectedIndex = min(max(index, 0), len(m.results)-1)
	m.updateViewport()
	if m.previewVisible() {
		m.updatePreview()
	}
}

// formatResultLine formats a single search result for display.
func (m *SearchResultsModel) formatResultLine(index int, result SearchResult) string {
	isSelected := index == m.selectedIndex

	// Styles
	selectedStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		Background(ColorBorder)
	normalStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	pathStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	lineNumStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)
	matchStyle := lipgloss.NewStyle().
		Foreground(ColorWarning)

	// Format path
	displayPath := safeKeyEntryText(result.FilePath)
	if displayPath == "" {
		displayPath = "(unnamed result)"
	}
	var lineText string
	if result.LineNumber > 0 {
		lineText = fmt.Sprintf(":%d", result.LineNumber)
	}

	var matchText string
	if result.MatchCount > 1 {
		matchText = fmt.Sprintf(" (%d matches)", result.MatchCount)
	}

	prefix := "  "
	if isSelected {
		prefix = "> "
	}

	// Add content preview on the same line if short
	contentPreview := safeKeyEntryText(result.Content)
	if len([]rune(contentPreview)) >= 50 {
		contentPreview = ""
	}

	// Reserve the right edge for line/match metadata. Paths carry their most
	// identifying information at the end, so truncate them from the left.
	available := max(m.viewport.Width-lipgloss.Width(prefix), 1)
	if lipgloss.Width(lineText+matchText) > max(available-4, 0) {
		matchText = ""
	}
	contentText := ""
	metaWidth := lipgloss.Width(lineText + matchText)
	if contentPreview != "" && available-metaWidth >= 32 {
		contentBudget := min(24, available-metaWidth-14)
		contentText = "  " + ansi.Truncate(contentPreview, max(contentBudget, 1), "…")
	}
	pathBudget := available - metaWidth - lipgloss.Width(contentText) - 1
	if pathBudget < 1 {
		contentText = ""
		pathBudget = max(available-metaWidth-1, 1)
	}
	displayPath = truncateLeftForWidth(displayPath, pathBudget)

	line := prefix + pathStyle.Render(displayPath) +
		lineNumStyle.Render(lineText) + matchStyle.Render(matchText)
	if contentText != "" {
		line += lineNumStyle.Render(contentText)
	}
	line = ansi.Truncate(line, max(m.viewport.Width, 1), "…")

	if isSelected {
		return selectedStyle.Render(line)
	}
	return normalStyle.Render(line)
}

// updatePreview updates the preview pane with context.
func (m *SearchResultsModel) updatePreview() {
	if len(m.results) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.results) {
		m.previewPane.SetContent("")
		m.previewPane.GotoTop()
		return
	}

	result := m.results[m.selectedIndex]

	var content strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight)
	displayPath := safeKeyEntryText(result.FilePath)
	if displayPath == "" {
		displayPath = "(unnamed result)"
	}
	content.WriteString(headerStyle.Render(filepath.Base(displayPath)))
	content.WriteString("\n\n")

	// Context lines
	lineNumStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	matchStyle := lipgloss.NewStyle().
		Foreground(ColorWarning).
		Bold(true)

	contextLines := result.Context
	if len(contextLines) == 0 && result.Content != "" {
		contextLines = []string{result.Content}
	}
	if len(contextLines) == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		content.WriteString(dimStyle.Render("No preview available"))
		content.WriteString("\n")
	}
	for i, ctx := range contextLines {
		ctx = strings.ReplaceAll(safeTerminalDisplayText(ctx), "\n", " ")
		lineNum := result.LineNumber - len(contextLines)/2 + i
		if lineNum < 1 {
			lineNum = 1
		}

		isMatchLine := i == len(contextLines)/2

		numStr := lineNumStyle.Render(fmt.Sprintf("%4d │ ", lineNum))
		if isMatchLine {
			content.WriteString(numStr)
			content.WriteString(matchStyle.Render(ctx))
		} else {
			content.WriteString(numStr)
			content.WriteString(ctx)
		}
		content.WriteString("\n")
	}

	m.previewPane.SetContent(content.String())
	m.previewPane.GotoTop()
}

// Init initializes the search results model.
func (m SearchResultsModel) Init() tea.Cmd {
	return nil
}

// Update handles input events for search results.
func (m SearchResultsModel) Update(msg tea.Msg) (SearchResultsModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.actionPending && searchTerminalActionKey(msg.String()) {
			return m, nil
		}
		switch msg.String() {
		case "j", "down":
			if m.selectedIndex < len(m.results)-1 {
				m.selectIndex(m.selectedIndex + 1)
			}

		case "k", "up":
			if m.selectedIndex > 0 {
				m.selectIndex(m.selectedIndex - 1)
			}

		case "home":
			m.selectIndex(0)

		case "end":
			m.selectIndex(len(m.results) - 1)

		case "pgup":
			m.selectIndex(m.selectedIndex - max(m.viewport.Height, 1))

		case "pgdown":
			m.selectIndex(m.selectedIndex + max(m.viewport.Height, 1))

		case "enter":
			if workspaceTargetActionsUnreadable(m.width, m.height, minSearchTargetWidth) {
				return m, nil
			}
			if m.actionsAvailable() && len(m.results) > 0 && m.selectedIndex < len(m.results) {
				result := m.results[m.selectedIndex]
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(SearchActionOpen, result.FilePath, result.LineNumber)
				}
				return m, func() tea.Msg {
					return SearchResultsActionMsg{
						Action:          SearchActionOpen,
						FilePath:        result.FilePath,
						LineNumber:      result.LineNumber,
						ownerGeneration: m.ownerGeneration,
						triggerKey:      msg.String(),
					}
				}
			}

		case "e":
			if workspaceTargetActionsUnreadable(m.width, m.height, minSearchTargetWidth) {
				return m, nil
			}
			if m.actionsAvailable() && len(m.results) > 0 && m.selectedIndex < len(m.results) {
				result := m.results[m.selectedIndex]
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(SearchActionEdit, result.FilePath, result.LineNumber)
				}
				return m, func() tea.Msg {
					return SearchResultsActionMsg{
						Action:          SearchActionEdit,
						FilePath:        result.FilePath,
						LineNumber:      result.LineNumber,
						ownerGeneration: m.ownerGeneration,
						triggerKey:      msg.String(),
					}
				}
			}

		case " ":
			// Toggle preview
			if len(m.results) > 0 {
				m.showPreview = !m.showPreview
				m.SetSize(m.width, m.height)
				if m.showPreview {
					m.updatePreview()
				}
			}

		case "ctrl+j":
			if m.previewVisible() {
				m.previewPane.ScrollDown(3)
			}

		case "ctrl+k":
			if m.previewVisible() {
				m.previewPane.ScrollUp(3)
			}

		case "y":
			// Copy path
			if workspaceTargetActionsUnreadable(m.width, m.height, minSearchTargetWidth) {
				return m, nil
			}
			if len(m.results) > 0 && m.selectedIndex < len(m.results) {
				result := m.results[m.selectedIndex]
				if m.onAction != nil {
					m.onAction(SearchActionCopyPath, result.FilePath, 0)
				}
				return m, func() tea.Msg {
					return SearchResultsActionMsg{
						Action:          SearchActionCopyPath,
						FilePath:        result.FilePath,
						ownerGeneration: m.ownerGeneration,
					}
				}
			}

		case "q", "esc":
			m.actionPending = true
			if m.onAction != nil {
				m.onAction(SearchActionClose, "", 0)
			}
			return m, func() tea.Msg {
				return SearchResultsActionMsg{Action: SearchActionClose, ownerGeneration: m.ownerGeneration, triggerKey: msg.String()}
			}

		case "g":
			m.selectIndex(0)

		case "G":
			if len(m.results) > 0 {
				m.selectIndex(len(m.results) - 1)
			}
		}

	case tea.MouseMsg:
		if m.canScrollPreview() {
			m.previewPane, cmd = m.previewPane.Update(msg)
		} else if direction := verticalMouseWheelDirection(msg); direction != 0 {
			// A short preview has nothing to scroll, so preserve mouse/keyboard
			// parity by moving the result selection. Keep the highlighted result
			// and viewport coupled rather than scrolling the viewport alone.
			step := max(m.viewport.MouseWheelDelta, 1)
			m.selectIndex(m.selectedIndex + direction*step)
		} else if m.previewVisible() {
			m.previewPane, cmd = m.previewPane.Update(msg)
		} else {
			m.viewport, cmd = m.viewport.Update(msg)
		}
		return m, cmd

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	}

	return m, nil
}

func searchTerminalActionKey(key string) bool {
	switch key {
	case "enter", "e", "q", "esc":
		return true
	}
	return false
}

// View renders the search results.
func (m SearchResultsModel) View() string {
	var builder strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Padding(0, 1)

	builder.WriteString(headerStyle.Render("Search Results"))
	builder.WriteString("\n\n")

	// Query info
	queryStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	countStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)

	toolLabel := strings.TrimSpace(m.tool)
	if toolLabel == "" {
		toolLabel = "Search"
	}
	resultLabel := "results"
	if len(m.results) == 1 {
		resultLabel = "result"
	}
	queryLine := fmt.Sprintf("%s: %s  %s",
		toolLabel,
		queryStyle.Render(m.query),
		countStyle.Render(fmt.Sprintf("(%d %s)", len(m.results), resultLabel)),
	)
	builder.WriteString("  ")
	builder.WriteString(ansi.Truncate(queryLine, max(m.renderWidth()-2, 1), "…"))
	builder.WriteString("\n\n")

	// Results viewport with border
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	if m.previewVisible() && m.width >= minSearchResultsSplitWidth {
		// Split view
		resultBox := borderStyle.Width(m.viewport.Width + 2).Render(m.viewport.View())
		previewView := fitPanelContent(m.previewPane.View(), m.previewPane.Width)
		previewBox := borderStyle.Width(m.previewPane.Width + 2).Render(previewView)
		builder.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, resultBox, " ", previewBox))
	} else if m.previewVisible() {
		previewView := fitPanelContent(m.previewPane.View(), m.previewPane.Width)
		builder.WriteString(borderStyle.Width(m.previewPane.Width + 2).Render(previewView))
	} else {
		builder.WriteString(borderStyle.Width(m.viewport.Width + 2).Render(m.viewport.View()))
	}

	builder.WriteString("\n\n")

	// Footer with actions
	m.renderActions(&builder)

	return builder.String()
}

// renderActions renders the available actions.
func (m *SearchResultsModel) renderActions(builder *strings.Builder) {
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Bold(true)

	if len(m.results) == 0 {
		builder.WriteString(ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc/q")+" Close"), m.renderWidth(), "…"))
		return
	}
	if workspaceTargetActionsUnreadable(m.width, m.height, minSearchTargetWidth) {
		if m.canNavigateResults() {
			builder.WriteString(ansi.Truncate(hintStyle.Render("↑/↓ Navigate"), m.renderWidth(), "…"))
			builder.WriteString("\n")
		}
		builder.WriteString(ansi.Truncate(hintStyle.Render(tinyWorkspaceResizeRecoveryHint(m.renderWidth())), m.renderWidth(), "…"))
		return
	}

	previewAction := "Show preview"
	if m.showPreview {
		previewAction = "Hide preview"
	}
	hints := []string{
		keyStyle.Render("Esc/q") + " Close",
		keyStyle.Render("Space") + " " + previewAction,
		keyStyle.Render("y") + " Copy path",
	}
	if m.actionsAvailable() {
		hints = append(hints, keyStyle.Render("Enter")+" Open", keyStyle.Render("e")+" Edit")
	} else {
		hints = append(hints, hintStyle.Render("Open/Edit unavailable"))
	}

	builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(hints, "  │  ")), m.renderWidth(), "…"))
	var secondary []string
	if m.previewVisible() {
		if m.canScrollPreview() {
			secondary = append(secondary, "Ctrl+j/k Scroll preview")
		}
		if m.canNavigateResults() {
			secondary = append(secondary, "↑/↓ Change result")
			if m.canPageResults() {
				secondary = append(secondary, "PgUp/PgDn Page results")
			}
			secondary = append(secondary, "Home/End Jump")
		}
	} else if m.canNavigateResults() {
		secondary = append(secondary, "↑/↓ Navigate")
		if m.canPageResults() {
			secondary = append(secondary, "PgUp/PgDn Page")
		}
		secondary = append(secondary, "Home/End Jump")
	}
	if len(secondary) > 0 {
		builder.WriteString("\n")
		builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(secondary, "  │  ")), m.renderWidth(), "…"))
	}
}

// GetSelectedResult returns the currently selected result.
func (m SearchResultsModel) GetSelectedResult() (SearchResult, bool) {
	if len(m.results) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.results) {
		return SearchResult{}, false
	}
	return m.results[m.selectedIndex], true
}

// truncate truncates a string to max length (rune-safe).
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 || len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen]) // no room for ellipsis; avoids maxLen-3 underflow panic
	}
	return string(runes[:maxLen-3]) + "..."
}
