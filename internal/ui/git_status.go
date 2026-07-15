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

// GitAction represents user actions on git status.
type GitAction int

const (
	GitActionNone GitAction = iota
	GitActionStage
	GitActionUnstage
	GitActionStageAll
	GitActionUnstageAll
	GitActionDiff
	GitActionCommit
	GitActionReset
	GitActionClose
)

// GitFileStatus represents the status of a file in git.
type GitFileStatus int

const (
	GitFileUntracked GitFileStatus = iota
	GitFileModified
	GitFileStaged
	GitFileDeleted
	GitFileRenamed
	GitFileConflict
)

// GitFileEntry represents a file in git status.
type GitFileEntry struct {
	FilePath  string
	Status    GitFileStatus
	IsStaged  bool
	OldPath   string // For renames
	DiffStats string // e.g., "+10 -5"
}

// GitStatusRequestMsg is sent to display git status.
type GitStatusRequestMsg struct {
	Entries     []GitFileEntry
	Branch      string
	Upstream    string
	AheadBehind string // e.g., "2 ahead, 1 behind"
}

// GitStatusActionMsg is sent when user performs an action.
type GitStatusActionMsg struct {
	Action          GitAction
	Files           []string
	Message         string
	RequestID       string
	ownerGeneration uint64
	triggerKey      string
}

// GitStatusDiffMsg delivers an asynchronously loaded inline diff. FilePath is
// required so late responses cannot replace the preview for a newer selection.
type GitStatusDiffMsg struct {
	RequestID string
	FilePath  string
	Content   string
	Error     string
}

// GitStatusModel is the UI for interactive git status.
type GitStatusModel struct {
	entries              []GitFileEntry
	selectedIndex        int
	selectedIndices      map[int]bool // For multi-select
	entryRows            map[int]int  // Entry index to rendered viewport row
	viewport             viewport.Model
	diffViewport         viewport.Model
	showDiff             bool
	pendingDiffPath      string
	pendingDiffRequestID string
	diffRequestSeq       uint64
	diffLoadError        bool
	confirmReset         bool
	pendingResetFiles    []string
	branch               string
	upstream             string
	aheadBehind          string
	styles               *Styles
	width                int
	height               int
	actionsLinked        bool
	actionsKnown         bool
	ownerGeneration      uint64
	actionPending        bool

	// Callback for actions
	onAction func(action GitAction, files []string, message string)
}

func (m *GitStatusModel) setOwnerGeneration(generation uint64) {
	m.ownerGeneration = generation
}

const minGitStatusSplitWidth = 64

// Cursor, selection marker and status consume six cells before the path. The
// outer panel consumes four more, so eleven terminal columns are the first
// geometry capable of showing any target identity at all.
const minGitTargetWidth = 11

// NewGitStatusModel creates a new git status model.
func NewGitStatusModel(styles *Styles) GitStatusModel {
	vp := viewport.New(60, 15)
	vp.MouseWheelEnabled = true

	diffVp := viewport.New(60, 15)
	diffVp.MouseWheelEnabled = true

	return GitStatusModel{
		viewport:        vp,
		diffViewport:    diffVp,
		styles:          styles,
		selectedIndices: make(map[int]bool),
		entryRows:       make(map[int]int),
	}
}

// SetSize sets the size of the git status view.
func (m *GitStatusModel) SetSize(width, height int) {
	m.width = max(width, 0)
	m.height = max(height, 0)
	chromeRows := 11
	if m.height < 14 {
		chromeRows = 10 // compact title spacing keeps both diff context and footer visible
	}
	contentHeight := max(m.height-chromeRows, 1)

	if m.diffVisible() && m.width >= minGitStatusSplitWidth {
		listOuterWidth := (m.width - 1) / 2
		diffOuterWidth := m.width - listOuterWidth - 1
		m.viewport.Width = max(listOuterWidth-4, 1)
		m.diffViewport.Width = max(diffOuterWidth-4, 1)
	} else {
		panelWidth := max(m.width-4, 1)
		m.viewport.Width = panelWidth
		m.diffViewport.Width = panelWidth
	}
	m.viewport.Height = contentHeight
	m.diffViewport.Height = contentHeight
	if m.diffVisible() {
		// Reserve one row for the selected-file context header. On narrow
		// terminals the diff replaces the file list entirely, so without this
		// header ↑/↓ changed files invisibly.
		// Keeping the remaining rows in the viewport also makes the split panes
		// exactly equal in height: header + diff body matches the file-list body.
		m.diffViewport.Height = max(contentHeight-1, 1)
	}
	m.ensureSelectionVisible()
}

func (m GitStatusModel) renderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return max(m.viewport.Width+4, minGitStatusSplitWidth)
}

// SetStatus sets the git status to display.
func (m *GitStatusModel) SetStatus(entries []GitFileEntry, branch, upstream, aheadBehind string) {
	m.actionPending = false
	m.entries = append([]GitFileEntry(nil), entries...)
	m.branch = safeKeyEntryText(branch)
	m.upstream = safeKeyEntryText(upstream)
	m.aheadBehind = safeKeyEntryText(aheadBehind)
	m.selectedIndex = 0
	if ordered := m.orderedIndices(); len(ordered) > 0 {
		m.selectedIndex = ordered[0]
	}
	m.selectedIndices = make(map[int]bool)
	m.showDiff = false
	m.pendingDiffPath = ""
	m.pendingDiffRequestID = ""
	m.diffLoadError = false
	m.confirmReset = false
	m.pendingResetFiles = nil
	m.viewport.GotoTop()
	m.diffViewport.SetContent("")
	m.diffViewport.GotoTop()
	m.updateViewport()
	if m.width > 0 && m.height > 0 {
		m.SetSize(m.width, m.height)
	}
}

// SetDiff sets the diff shown for the selected file.
func (m *GitStatusModel) SetDiff(content string) {
	m.diffLoadError = false
	content = safeTerminalDisplayText(content)
	if strings.TrimSpace(content) == "" {
		content = "No diff available for this file"
	}
	m.diffViewport.SetContent(content)
	m.diffViewport.GotoTop()
}

// SetDiffError keeps the preview open and marks it retryable. The selected
// file remains stable, so `d` can repeat the same async request in place.
func (m *GitStatusModel) SetDiffError(message string) {
	m.diffLoadError = true
	message = safeKeyEntryText(message)
	if message == "" {
		message = "Unable to load diff"
	}
	m.diffViewport.SetContent(message + "\nPress d to retry")
	m.diffViewport.GotoTop()
}

// SetActionCallback sets the callback for user actions.
func (m *GitStatusModel) SetActionCallback(callback func(GitAction, []string, string)) {
	m.onAction = callback
}

// SetActionsLinked tells the reusable panel whether its parent can execute Git
// mutations and load diffs. Without a handler the panel remains a navigable,
// explicitly read-only status viewer.
func (m *GitStatusModel) SetActionsLinked(linked bool) {
	m.actionsLinked = linked
	m.actionsKnown = true
	if !linked && m.onAction == nil {
		m.confirmReset = false
		m.pendingResetFiles = nil
	}
}

func (m GitStatusModel) actionsAvailable() bool {
	return !m.actionsKnown || m.actionsLinked || m.onAction != nil
}

func (m GitStatusModel) canNavigateEntries() bool {
	return len(m.entries) > 1
}

func (m GitStatusModel) canPageEntries() bool {
	return m.canNavigateEntries() && m.viewport.TotalLineCount() > m.viewport.Height
}

func (m GitStatusModel) canScrollDiff() bool {
	return m.diffVisible() && m.diffViewport.TotalLineCount() > m.diffViewport.Height
}

func (m GitStatusModel) navigationHints() []string {
	if !m.canNavigateEntries() {
		return nil
	}
	hints := []string{"↑/↓ Navigate"}
	if m.canPageEntries() {
		hints = append(hints, "PgUp/PgDn Page")
	}
	return append(hints, "Home/End Jump")
}

// updateViewport updates the viewport content.
func (m *GitStatusModel) updateViewport() {
	var content strings.Builder
	m.entryRows = make(map[int]int, len(m.entries))
	row := 0

	// Staged section
	staged := m.filterByStaged(true)
	if len(staged) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorSuccess)
		content.WriteString(headerStyle.Render("Staged Changes"))
		content.WriteString("\n")
		row++

		for _, idx := range staged {
			m.entryRows[idx] = row
			line := m.formatEntryLine(idx)
			content.WriteString(line)
			content.WriteString("\n")
			row++
		}
		if len(m.filterByStaged(false)) > 0 {
			content.WriteString("\n")
			row++
		}
	}

	// Unstaged section
	unstaged := m.filterByStaged(false)
	if len(unstaged) > 0 {
		headerStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorWarning)
		content.WriteString(headerStyle.Render("Unstaged Changes"))
		content.WriteString("\n")
		row++

		for _, idx := range unstaged {
			m.entryRows[idx] = row
			line := m.formatEntryLine(idx)
			content.WriteString(line)
			content.WriteString("\n")
			row++
		}
	}
	if len(m.entries) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		content.WriteString(emptyStyle.Render("Working tree clean\nNo staged or unstaged changes"))
		content.WriteString("\n")
	}

	m.viewport.SetContent(content.String())
	m.ensureSelectionVisible()
}

func (m *GitStatusModel) diffVisible() bool {
	return m.showDiff && len(m.entries) > 0
}

func (m GitStatusModel) renderDiffPane() string {
	if !m.diffVisible() || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		return m.diffViewport.View()
	}
	ordered := m.orderedIndices()
	position := m.selectedPosition() + 1
	prefix := fmt.Sprintf("Diff · %d/%d · ", position, len(ordered))
	pathBudget := max(m.diffViewport.Width-lipgloss.Width(prefix), 1)
	path := shortenPath(safeKeyEntryText(m.entries[m.selectedIndex].FilePath), pathBudget)
	header := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true).Render(prefix + path)
	return header + "\n" + m.diffViewport.View()
}

func (m *GitStatusModel) orderedIndices() []int {
	ordered := m.filterByStaged(true)
	return append(ordered, m.filterByStaged(false)...)
}

func (m *GitStatusModel) ensureSelectionVisible() {
	if len(m.entries) == 0 || m.viewport.Height <= 0 {
		m.viewport.SetYOffset(0)
		return
	}
	row, ok := m.entryRows[m.selectedIndex]
	if !ok {
		return
	}
	if row < m.viewport.YOffset {
		m.viewport.SetYOffset(row)
	} else if row >= m.viewport.YOffset+m.viewport.Height {
		m.viewport.SetYOffset(row - m.viewport.Height + 1)
	}
}

func (m *GitStatusModel) selectPosition(position int) tea.Cmd {
	ordered := m.orderedIndices()
	if len(ordered) == 0 {
		return nil
	}
	position = min(max(position, 0), len(ordered)-1)
	if m.selectedIndex == ordered[position] {
		return nil
	}
	m.selectedIndex = ordered[position]
	m.updateViewport()
	if m.diffVisible() {
		return m.requestSelectedDiff()
	}
	return nil
}

func (m *GitStatusModel) selectedPosition() int {
	for position, index := range m.orderedIndices() {
		if index == m.selectedIndex {
			return position
		}
	}
	return 0
}

func (m *GitStatusModel) requestSelectedDiff() tea.Cmd {
	if len(m.entries) == 0 || m.selectedIndex < 0 || m.selectedIndex >= len(m.entries) {
		m.diffViewport.SetContent("")
		return nil
	}
	m.SetDiff("Loading diff…")
	path := m.entries[m.selectedIndex].FilePath
	m.pendingDiffPath = path
	m.diffRequestSeq++
	if m.diffRequestSeq == 0 {
		m.diffRequestSeq++
	}
	requestID := fmt.Sprintf("git-diff-%d", m.diffRequestSeq)
	m.pendingDiffRequestID = requestID
	if m.onAction != nil {
		m.onAction(GitActionDiff, []string{path}, "")
	}
	return func() tea.Msg {
		return GitStatusActionMsg{Action: GitActionDiff, Files: []string{path}, RequestID: requestID, ownerGeneration: m.ownerGeneration}
	}
}

// filterByStaged returns indices of entries filtered by staged status.
func (m *GitStatusModel) filterByStaged(staged bool) []int {
	var result []int
	for i, entry := range m.entries {
		if entry.IsStaged == staged {
			result = append(result, i)
		}
	}
	return result
}

func (m *GitStatusModel) selectedFilesByStaged(staged bool) []string {
	if len(m.selectedIndices) == 0 {
		if m.selectedIndex >= 0 && m.selectedIndex < len(m.entries) && m.entries[m.selectedIndex].IsStaged == staged {
			return []string{m.entries[m.selectedIndex].FilePath}
		}
		return nil
	}

	var files []string
	for _, index := range m.orderedIndices() {
		if m.selectedIndices[index] && m.entries[index].IsStaged == staged {
			files = append(files, m.entries[index].FilePath)
		}
	}
	return files
}

// formatEntryLine formats a single git entry for display.
func (m *GitStatusModel) formatEntryLine(index int) string {
	entry := m.entries[index]
	isSelected := index == m.selectedIndex
	isMultiSelected := false
	if m.selectedIndices != nil {
		isMultiSelected = m.selectedIndices[index]
	}

	// Styles
	selectedStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		Background(ColorBorder)
	normalStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	pathStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	statsStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)

	// Status icon
	var statusIcon string
	var statusColor lipgloss.Color
	switch entry.Status {
	case GitFileUntracked:
		statusIcon = "?"
		statusColor = ColorMuted
	case GitFileModified:
		statusIcon = "M"
		statusColor = ColorWarning
	case GitFileStaged:
		statusIcon = "A"
		statusColor = ColorSuccess
	case GitFileDeleted:
		statusIcon = "D"
		statusColor = ColorError
	case GitFileRenamed:
		statusIcon = "R"
		statusColor = ColorInfo
	case GitFileConflict:
		statusIcon = "!"
		statusColor = ColorError
	}
	if statusIcon == "" {
		statusIcon = "·"
		statusColor = ColorMuted
	}

	statusStyle := lipgloss.NewStyle().
		Foreground(statusColor).
		Bold(true)

	// Cursor and multi-selection indicators remain independently visible.
	prefix := "  "
	if isSelected {
		prefix = "> "
	}
	checkbox := "  "
	if isMultiSelected {
		checkbox = "✓ "
	}

	// Path display
	displayPath := safeKeyEntryText(entry.FilePath)
	if displayPath == "" {
		displayPath = "(unnamed file)"
	}
	statsText := ""
	if stats := safeKeyEntryText(entry.DiffStats); stats != "" {
		statsText = " " + stats
	}

	oldText := ""
	if oldPath := safeKeyEntryText(entry.OldPath); oldPath != "" {
		oldText = fmt.Sprintf(" (from %s)", filepath.Base(oldPath))
	}

	fixed := prefix + checkbox + statusIcon + " "
	available := max(m.viewport.Width-lipgloss.Width(fixed), 1)
	// Rename source is useful context but lower priority than current path and
	// diff stats; omit it when it would squeeze the path below 12 cells.
	if available-lipgloss.Width(statsText+oldText) < 12 {
		oldText = ""
	}
	if available-lipgloss.Width(statsText) < 4 {
		statsText = ""
	}
	pathBudget := max(available-lipgloss.Width(statsText+oldText)-1, 1)
	displayPath = truncateLeftForWidth(displayPath, pathBudget)

	line := prefix + checkbox + statusStyle.Render(statusIcon) + " " +
		pathStyle.Render(displayPath) + statsStyle.Render(statsText+oldText)
	line = ansi.Truncate(line, max(m.viewport.Width, 1), "…")

	if isSelected {
		return selectedStyle.Render(line)
	}
	return normalStyle.Render(line)
}

// Init initializes the git status model.
func (m GitStatusModel) Init() tea.Cmd {
	return nil
}

// Update handles input events for git status.
func (m GitStatusModel) Update(msg tea.Msg) (GitStatusModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.actionPending && gitTerminalActionKey(msg.String()) {
			return m, nil
		}
		if m.confirmReset {
			if !m.actionsAvailable() {
				m.confirmReset = false
				m.pendingResetFiles = nil
				return m, nil
			}
			switch msg.String() {
			case "enter":
				if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
					return m, nil
				}
				files := append([]string(nil), m.pendingResetFiles...)
				m.confirmReset = false
				m.pendingResetFiles = nil
				if len(files) == 0 {
					return m, nil
				}
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(GitActionReset, files, "")
				}
				return m, func() tea.Msg {
					return GitStatusActionMsg{Action: GitActionReset, Files: files, ownerGeneration: m.ownerGeneration, triggerKey: msg.String()}
				}
			case "esc", "q", "n":
				m.confirmReset = false
				m.pendingResetFiles = nil
			}
			return m, nil
		}

		switch msg.String() {
		case "j", "down":
			return m, m.selectPosition(m.selectedPosition() + 1)

		case "k", "up":
			return m, m.selectPosition(m.selectedPosition() - 1)

		case "home", "g":
			return m, m.selectPosition(0)

		case "end", "G":
			return m, m.selectPosition(len(m.entries) - 1)

		case "pgup":
			return m, m.selectPosition(m.selectedPosition() - max(m.viewport.Height-2, 1))

		case "pgdown":
			return m, m.selectPosition(m.selectedPosition() + max(m.viewport.Height-2, 1))

		case "ctrl+j":
			if m.diffVisible() {
				m.diffViewport.ScrollDown(3)
			}

		case "ctrl+k":
			if m.diffVisible() {
				m.diffViewport.ScrollUp(3)
			}

		case " ":
			// Stage or unstage the current multi-selection group.
			if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
				return m, nil
			}
			if m.actionsAvailable() && len(m.entries) > 0 && m.selectedIndex < len(m.entries) {
				entry := m.entries[m.selectedIndex]
				var action GitAction
				if entry.IsStaged {
					action = GitActionUnstage
				} else {
					action = GitActionStage
				}
				files := m.selectedFilesByStaged(entry.IsStaged)
				if len(files) == 0 {
					files = []string{entry.FilePath}
				}
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(action, files, "")
				}
				return m, func() tea.Msg {
					return GitStatusActionMsg{
						Action:          action,
						Files:           files,
						ownerGeneration: m.ownerGeneration,
						triggerKey:      msg.String(),
					}
				}
			}

		case "a":
			// Stage all
			if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
				return m, nil
			}
			if !m.actionsAvailable() {
				return m, nil
			}
			var files []string
			for _, entry := range m.entries {
				if !entry.IsStaged {
					files = append(files, entry.FilePath)
				}
			}
			if len(files) > 0 {
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(GitActionStageAll, files, "")
				}
				return m, func() tea.Msg {
					return GitStatusActionMsg{
						Action:          GitActionStageAll,
						Files:           files,
						ownerGeneration: m.ownerGeneration,
						triggerKey:      msg.String(),
					}
				}
			}

		case "u":
			// Unstage all
			if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
				return m, nil
			}
			if !m.actionsAvailable() {
				return m, nil
			}
			var files []string
			for _, entry := range m.entries {
				if entry.IsStaged {
					files = append(files, entry.FilePath)
				}
			}
			if len(files) > 0 {
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(GitActionUnstageAll, files, "")
				}
				return m, func() tea.Msg {
					return GitStatusActionMsg{
						Action:          GitActionUnstageAll,
						Files:           files,
						ownerGeneration: m.ownerGeneration,
						triggerKey:      msg.String(),
					}
				}
			}

		case "d":
			// Toggle diff view
			if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
				return m, nil
			}
			if m.actionsAvailable() && len(m.entries) > 0 {
				if m.showDiff && m.diffLoadError {
					return m, m.requestSelectedDiff()
				}
				m.showDiff = !m.showDiff
				if !m.showDiff {
					m.pendingDiffPath = ""
					m.pendingDiffRequestID = ""
				}
				m.SetSize(m.width, m.height)
				if m.showDiff {
					return m, m.requestSelectedDiff()
				}
			}

		case "c":
			// Commit staged changes
			if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
				return m, nil
			}
			if !m.actionsAvailable() {
				return m, nil
			}
			var stagedFiles []string
			for _, entry := range m.entries {
				if entry.IsStaged {
					stagedFiles = append(stagedFiles, entry.FilePath)
				}
			}
			if len(stagedFiles) > 0 {
				m.actionPending = true
				if m.onAction != nil {
					m.onAction(GitActionCommit, stagedFiles, "")
				}
				return m, func() tea.Msg {
					return GitStatusActionMsg{
						Action:          GitActionCommit,
						Files:           stagedFiles,
						ownerGeneration: m.ownerGeneration,
						triggerKey:      msg.String(),
					}
				}
			}

		case "r":
			// Reset may discard local changes. Snapshot the current selection and
			// require an explicit second step before emitting the action.
			if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
				return m, nil
			}
			if m.actionsAvailable() && len(m.entries) > 0 && m.selectedIndex < len(m.entries) {
				files := m.GetSelectedFiles()
				if len(files) > 0 {
					m.confirmReset = true
					m.pendingResetFiles = append([]string(nil), files...)
				}
			}

		case "q", "esc":
			m.actionPending = true
			if m.onAction != nil {
				m.onAction(GitActionClose, nil, "")
			}
			return m, func() tea.Msg {
				return GitStatusActionMsg{Action: GitActionClose, ownerGeneration: m.ownerGeneration, triggerKey: msg.String()}
			}

		case "tab":
			// Toggle multi-select for current item
			if m.actionsAvailable() && len(m.entries) > 0 {
				if m.selectedIndices == nil {
					m.selectedIndices = make(map[int]bool)
				}
				if m.selectedIndices[m.selectedIndex] {
					delete(m.selectedIndices, m.selectedIndex)
				} else {
					m.selectedIndices[m.selectedIndex] = true
				}
				m.updateViewport()
				return m, m.selectPosition(m.selectedPosition() + 1)
			}
		}

	case tea.MouseMsg:
		if m.canScrollDiff() {
			m.diffViewport, cmd = m.diffViewport.Update(msg)
		} else if direction := verticalMouseWheelDirection(msg); direction != 0 {
			step := max(m.viewport.MouseWheelDelta, 1)
			cmd = m.selectPosition(m.selectedPosition() + direction*step)
		} else if m.diffVisible() {
			m.diffViewport, cmd = m.diffViewport.Update(msg)
		} else {
			m.viewport, cmd = m.viewport.Update(msg)
		}
		return m, cmd

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	}

	return m, nil
}

func gitTerminalActionKey(key string) bool {
	switch key {
	case "enter", " ", "a", "u", "c", "q", "esc":
		return true
	}
	return false
}

// View renders the git status.
func (m GitStatusModel) View() string {
	var builder strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Padding(0, 1)

	builder.WriteString(headerStyle.Render(MessageIcons["info"] + " Git Status"))
	if m.height > 0 && m.height < 14 {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n")
	}

	// Branch info
	branchStyle := lipgloss.NewStyle().
		Foreground(ColorSuccess).
		Bold(true)
	upstreamStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)
	aheadStyle := lipgloss.NewStyle().
		Foreground(ColorWarning)

	branch := strings.TrimSpace(m.branch)
	if branch == "" {
		branch = "unknown"
	}
	var branchLine strings.Builder
	fmt.Fprintf(&branchLine, "Branch: %s", branchStyle.Render(branch))
	if m.upstream != "" {
		fmt.Fprintf(&branchLine, " → %s", upstreamStyle.Render(m.upstream))
	}
	if m.aheadBehind != "" {
		fmt.Fprintf(&branchLine, " (%s)", aheadStyle.Render(m.aheadBehind))
	}
	builder.WriteString("  ")
	builder.WriteString(ansi.Truncate(branchLine.String(), max(m.renderWidth()-2, 1), "…"))
	builder.WriteString("\n\n")

	// File counts
	staged := len(m.filterByStaged(true))
	unstaged := len(m.filterByStaged(false))
	countStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	builder.WriteString(countStyle.Render(fmt.Sprintf("  Staged: %d  │  Unstaged: %d", staged, unstaged)))
	builder.WriteString("\n\n")

	// Content viewports
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	if m.diffVisible() && m.width >= minGitStatusSplitWidth {
		// Split view
		filesBox := borderStyle.Width(m.viewport.Width + 2).Render(m.viewport.View())
		diffView := fitPanelContent(m.renderDiffPane(), m.diffViewport.Width)
		diffBox := borderStyle.Width(m.diffViewport.Width + 2).Render(diffView)
		builder.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, filesBox, " ", diffBox))
	} else if m.diffVisible() {
		diffView := fitPanelContent(m.renderDiffPane(), m.diffViewport.Width)
		builder.WriteString(borderStyle.Width(m.diffViewport.Width + 2).Render(diffView))
	} else {
		builder.WriteString(borderStyle.Width(m.viewport.Width + 2).Render(m.viewport.View()))
	}

	builder.WriteString("\n\n")

	// Footer with actions
	m.renderActions(&builder)

	return builder.String()
}

// renderActions renders the available actions.
func (m *GitStatusModel) renderActions(builder *strings.Builder) {
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Bold(true)

	if len(m.entries) == 0 {
		builder.WriteString(ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc/q")+" Close"), m.renderWidth(), "…"))
		return
	}
	if workspaceTargetActionsUnreadable(m.width, m.height, minGitTargetWidth) {
		if navigation := m.navigationHints(); len(navigation) > 0 {
			builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(navigation, "  │  ")), m.renderWidth(), "…"))
			builder.WriteString("\n")
		}
		builder.WriteString(ansi.Truncate(hintStyle.Render(tinyWorkspaceResizeRecoveryHint(m.renderWidth())), m.renderWidth(), "…"))
		return
	}
	if m.confirmReset {
		warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		count := len(m.pendingResetFiles)
		target := fmt.Sprintf("%d files", count)
		if count == 1 {
			name := filepath.Base(safeKeyEntryText(m.pendingResetFiles[0]))
			if name == "" || name == "." {
				name = "this file"
			}
			target = name
		}
		warning := fmt.Sprintf("Reset %s? Local changes may be lost", target)
		builder.WriteString(ansi.Truncate(warningStyle.Render(warning), m.renderWidth(), "…"))
		builder.WriteString("\n")
		// Cancellation is the fail-safe action; keep it first so narrow-terminal
		// truncation can never advertise only the destructive confirmation.
		controls := keyStyle.Render("Esc/q") + " Cancel  │  " + keyStyle.Render("Enter") + " Confirm reset"
		builder.WriteString(ansi.Truncate(hintStyle.Render(controls), m.renderWidth(), "…"))
		return
	}
	if !m.actionsAvailable() {
		primary := keyStyle.Render("Esc/q") + " Close  │  " + hintStyle.Render("Read-only · Git actions unavailable")
		builder.WriteString(ansi.Truncate(hintStyle.Render(primary), m.renderWidth(), "…"))
		if navigation := m.navigationHints(); len(navigation) > 0 {
			builder.WriteString("\n")
			builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(navigation, "  │  ")), m.renderWidth(), "…"))
		}
		return
	}

	staged := len(m.filterByStaged(true))
	unstaged := len(m.entries) - staged
	diffAction := "Show diff"
	if m.showDiff {
		diffAction = "Hide diff"
	}
	if m.showDiff && m.diffLoadError {
		diffAction = "Retry diff"
	}
	hints := []string{keyStyle.Render("Esc/q") + " Close", keyStyle.Render("Space") + " Stage/Unstage"}
	if unstaged > 0 {
		hints = append(hints, keyStyle.Render("a")+" Stage all")
	}
	if staged > 0 {
		hints = append(hints, keyStyle.Render("u")+" Unstage all", keyStyle.Render("c")+" Commit")
	}
	hints = append(hints,
		keyStyle.Render("d")+" "+diffAction,
		keyStyle.Render("r")+" Reset",
	)

	builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(hints, "  │  ")), m.renderWidth(), "…"))
	secondary := make([]string, 0, 4)
	if m.canScrollDiff() {
		secondary = append(secondary, "Ctrl+j/k Scroll diff")
	}
	secondary = append(secondary, m.navigationHints()...)
	selectionHint := "Tab Select"
	if len(m.selectedIndices) > 0 {
		selectionHint = fmt.Sprintf("Tab Select (%d marked)", len(m.selectedIndices))
	}
	if m.canNavigateEntries() || len(m.selectedIndices) > 0 {
		secondary = append(secondary, selectionHint)
	}
	if len(secondary) > 0 {
		builder.WriteString("\n")
		builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(secondary, "  │  ")), m.renderWidth(), "…"))
	}
}

// GetSelectedFiles returns the currently selected files.
func (m GitStatusModel) GetSelectedFiles() []string {
	if len(m.selectedIndices) > 0 {
		var files []string
		for _, idx := range m.orderedIndices() {
			if m.selectedIndices[idx] {
				files = append(files, m.entries[idx].FilePath)
			}
		}
		return files
	}

	if m.selectedIndex >= 0 && m.selectedIndex < len(m.entries) {
		return []string{m.entries[m.selectedIndex].FilePath}
	}
	return nil
}
