package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"gokin/internal/highlight"
)

// FileBrowserAction represents user actions in the file browser.
type FileBrowserAction int

const (
	FileBrowserActionNone FileBrowserAction = iota
	FileBrowserActionOpen
	FileBrowserActionSelect
	FileBrowserActionClose
)

// FileEntry represents a file or directory entry.
type FileEntry struct {
	Name     string
	Path     string
	IsDir    bool
	Size     int64
	ModTime  string
	Mode     os.FileMode
	IsHidden bool
}

// FileBrowserRequestMsg is sent to open the file browser.
type FileBrowserRequestMsg struct {
	StartPath string
}

// FileBrowserActionMsg is sent when user performs an action.
type FileBrowserActionMsg struct {
	Action FileBrowserAction
	Path   string
	Files  []string // For multi-select
}

// FileBrowserModel is the UI for interactive file browsing.
type FileBrowserModel struct {
	currentDir    string
	entries       []FileEntry
	selectedIndex int
	selectedFiles map[string]bool // For multi-select
	filter        string
	showHidden    bool
	viewport      viewport.Model
	styles        *Styles
	width         int
	height        int

	// Search/filter state
	filterInput  string
	filterActive bool

	// Error message (cleared when the user presses the next key)
	errorMsg string

	// Preview panel
	previewEnabled     bool
	previewContent     string
	previewFilePath    string
	previewViewport    viewport.Model
	previewHighlighter *highlight.Highlighter
	previewMaxLines    int // Max lines to preview (default: 100)
	previewLoadError   string
	previewRecovery    string

	// Split dimensions
	listWidth    int
	previewWidth int

	// Callback for actions
	onAction func(action FileBrowserAction, path string, files []string)
}

const minFileBrowserSplitWidth = 64

func (m FileBrowserModel) previewSupported() bool {
	return m.width <= 0 || m.width >= minFileBrowserSplitWidth
}

// NewFileBrowserModel creates a new file browser model.
func NewFileBrowserModel(styles *Styles) FileBrowserModel {
	vp := viewport.New(60, 20)
	vp.MouseWheelEnabled = true

	previewVp := viewport.New(60, 20)
	previewVp.MouseWheelEnabled = true

	return FileBrowserModel{
		viewport:           vp,
		styles:             styles,
		selectedFiles:      make(map[string]bool),
		showHidden:         false,
		previewEnabled:     false,
		previewViewport:    previewVp,
		previewHighlighter: highlight.New("monokai"),
		previewMaxLines:    100,
	}
}

// SetSize sets the size of the file browser.
func (m *FileBrowserModel) SetSize(width, height int) {
	m.width = max(width, 1)
	m.height = max(height, 1)
	m.updateLayout()
	m.updateViewport()
	if m.previewEnabled && len(m.entries) > 0 {
		m.loadPreview(m.entries[m.selectedIndex])
	}

	m.ensureSelectionVisible()
}

// updateLayout keeps the bordered panels inside the real terminal dimensions.
// Width values stored here are content widths; borders and horizontal padding
// add four cells per panel.
func (m *FileBrowserModel) updateLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	extraRows := 0
	if len(m.selectedFiles) > 0 {
		extraRows++
	}
	if m.filter != "" || m.filterActive {
		extraRows++
	}
	if m.errorMsg != "" {
		extraRows++
	}
	contentHeight := max(m.height-10-extraRows, 1)
	m.viewport.Height = contentHeight

	if m.previewEnabled && m.width >= minFileBrowserSplitWidth {
		listOuterWidth := max(m.width*40/100, 28)
		previewOuterWidth := m.width - listOuterWidth - 1
		if previewOuterWidth < 28 {
			previewOuterWidth = 28
			listOuterWidth = m.width - previewOuterWidth - 1
		}
		m.listWidth = max(listOuterWidth-4, 1)
		m.previewWidth = max(previewOuterWidth-4, 1)
		m.viewport.Width = m.listWidth
		m.previewViewport.Width = m.previewWidth
	} else {
		m.listWidth = max(m.width-4, 1)
		m.previewWidth = 0
		m.viewport.Width = m.listWidth
		m.previewViewport.Width = m.listWidth
	}
	m.previewViewport.Height = max(contentHeight-2, 1)
	m.ensureSelectionVisible()
}

func (m FileBrowserModel) renderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return max(m.viewport.Width+4, minFileBrowserSplitWidth)
}

// SetPath sets the current directory path.
func (m *FileBrowserModel) SetPath(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		absPath = filepath.Dir(absPath)
	}

	oldDir := m.currentDir
	oldIndex := m.selectedIndex
	oldSelectedFiles := m.selectedFiles
	oldFilter := m.filter
	oldFilterInput := m.filterInput
	m.currentDir = absPath
	m.selectedIndex = 0
	m.selectedFiles = make(map[string]bool)
	m.filter = ""
	m.filterInput = ""

	if err := m.loadEntries(); err != nil {
		m.currentDir = oldDir
		m.selectedIndex = oldIndex
		m.selectedFiles = oldSelectedFiles
		m.filter = oldFilter
		m.filterInput = oldFilterInput
		m.updateLayout()
		m.updateViewport()
		return err
	}
	return nil
}

// loadEntries loads the directory entries.
func (m *FileBrowserModel) loadEntries() error {
	selectedPath := ""
	if m.selectedIndex >= 0 && m.selectedIndex < len(m.entries) {
		selectedPath = m.entries[m.selectedIndex].Path
	}
	entries, err := os.ReadDir(m.currentDir)
	if err != nil {
		return err
	}

	m.entries = make([]FileEntry, 0, len(entries)+1)

	// Add parent directory if not at root
	if m.currentDir != "/" {
		m.entries = append(m.entries, FileEntry{
			Name:  "..",
			Path:  filepath.Dir(m.currentDir),
			IsDir: true,
		})
	}

	for _, entry := range entries {
		name := entry.Name()
		isHidden := strings.HasPrefix(name, ".")

		if !m.showHidden && isHidden {
			continue
		}

		// Apply filter
		if m.filter != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(m.filter)) {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		fe := FileEntry{
			Name:     name,
			Path:     filepath.Join(m.currentDir, name),
			IsDir:    entry.IsDir(),
			Size:     info.Size(),
			ModTime:  info.ModTime().Format("Jan 02 15:04"),
			Mode:     info.Mode(),
			IsHidden: isHidden,
		}
		m.entries = append(m.entries, fe)
	}

	// Sort: directories first, then alphabetically
	sort.Slice(m.entries, func(i, j int) bool {
		if m.entries[i].Name == ".." {
			return true
		}
		if m.entries[j].Name == ".." {
			return false
		}
		if m.entries[i].IsDir != m.entries[j].IsDir {
			return m.entries[i].IsDir
		}
		return strings.ToLower(m.entries[i].Name) < strings.ToLower(m.entries[j].Name)
	})
	if selectedPath != "" {
		for i := range m.entries {
			if m.entries[i].Path == selectedPath {
				m.selectedIndex = i
				break
			}
		}
	}

	m.syncSelection()
	m.updateLayout()
	m.updateViewport()
	return nil
}

// syncSelection keeps the cursor and preview consistent after filtering,
// toggling hidden files, or changing directories.
func (m *FileBrowserModel) syncSelection() {
	if len(m.entries) == 0 {
		m.selectedIndex = 0
		m.previewContent = ""
		m.previewFilePath = ""
		m.previewLoadError = ""
		m.previewRecovery = ""
		m.previewViewport.SetContent("")
		return
	}

	if m.selectedIndex >= len(m.entries) {
		m.selectedIndex = len(m.entries) - 1
	}
	if m.selectedIndex < 0 {
		m.selectedIndex = 0
	}

	if m.previewEnabled {
		m.loadPreview(m.entries[m.selectedIndex])
	}
}

// selectEntry is the single cursor transaction for arrows, paging and boundary
// jumps. Keeping viewport visibility and preview refresh here prevents the
// faster navigation keys from moving a hidden cursor or leaving stale content.
func (m *FileBrowserModel) selectEntry(index int) {
	if len(m.entries) == 0 {
		m.selectedIndex = 0
		m.updateViewport()
		return
	}
	index = min(max(index, 0), len(m.entries)-1)
	changed := index != m.selectedIndex
	m.selectedIndex = index
	m.updateViewport()
	if changed && m.previewEnabled {
		m.loadPreview(m.entries[m.selectedIndex])
	}
}

// SetActionCallback sets the callback for user actions.
func (m *FileBrowserModel) SetActionCallback(callback func(FileBrowserAction, string, []string)) {
	m.onAction = callback
}

// loadPreview loads and highlights file content for preview.
func (m *FileBrowserModel) loadPreview(entry FileEntry) {
	m.previewLoadError = ""
	m.previewRecovery = ""
	m.previewContent = ""
	m.previewFilePath = ""

	if entry.IsDir || entry.Name == ".." {
		m.previewContent = ""
		return
	}
	m.previewFilePath = entry.Path

	// Size check (max 1MB)
	const maxPreviewSize = 1024 * 1024
	if entry.Size > maxPreviewSize {
		m.previewLoadError = "File too large for preview"
		m.previewRecovery = "Enter Add to draft"
		return
	}

	content, err := os.ReadFile(entry.Path)
	if err != nil {
		m.previewLoadError = fmt.Sprintf("Cannot read: %s", safeKeyEntryText(err.Error()))
		m.previewRecovery = "r Retry preview"
		return
	}

	// Binary check
	if isBinaryContent(content) {
		m.previewLoadError = "Binary file"
		m.previewRecovery = "Enter Add to draft"
		return
	}

	// Truncate to maxLines
	safeContent := safeTerminalDisplayText(string(content))
	lines := strings.Split(safeContent, "\n")
	if len(lines) > m.previewMaxLines {
		lines = lines[:m.previewMaxLines]
		lines = append(lines, fmt.Sprintf("... (%d more lines)", len(strings.Split(string(content), "\n"))-m.previewMaxLines))
	}

	// Detect language and highlight
	lang := m.previewHighlighter.DetectLanguage(entry.Name)
	m.previewContent = m.previewHighlighter.HighlightWithLineNumbers(
		strings.Join(lines, "\n"), lang, 1,
	)
	m.previewViewport.SetContent(m.previewContent)
	m.previewViewport.GotoTop()
}

// isBinaryContent checks if content is binary by looking for null bytes.
func isBinaryContent(content []byte) bool {
	checkLen := 512
	if len(content) < checkLen {
		checkLen = len(content)
	}
	for i := 0; i < checkLen; i++ {
		if content[i] == 0 {
			return true
		}
	}
	return false
}

// updateViewport updates the viewport content.
func (m *FileBrowserModel) updateViewport() {
	var content strings.Builder

	for i, entry := range m.entries {
		line := m.formatEntryLine(i, entry)
		content.WriteString(line)
		content.WriteString("\n")
	}

	if emptyState := m.emptyStateText(); emptyState != "" {
		if content.Len() > 0 {
			content.WriteString("\n")
		}
		emptyStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		content.WriteString(emptyStyle.Render(emptyState))
		content.WriteString("\n")
	}

	m.viewport.SetContent(content.String())
	m.ensureSelectionVisible()
}

// ensureSelectionVisible scrolls the list just enough to keep the cursor on
// screen. Without this, navigation still changes the selection but it becomes
// invisible as soon as it moves past the first viewport page.
func (m *FileBrowserModel) ensureSelectionVisible() {
	if len(m.entries) == 0 || m.viewport.Height <= 0 {
		m.viewport.SetYOffset(0)
		return
	}

	if m.selectedIndex < m.viewport.YOffset {
		m.viewport.SetYOffset(m.selectedIndex)
		return
	}
	if m.selectedIndex >= m.viewport.YOffset+m.viewport.Height {
		m.viewport.SetYOffset(m.selectedIndex - m.viewport.Height + 1)
	}
}

// emptyStateText describes why there are no browsable results. The parent
// directory entry is navigation, not a search result, so it is ignored here.
func (m *FileBrowserModel) emptyStateText() string {
	for _, entry := range m.entries {
		if entry.Name != ".." {
			return ""
		}
	}

	if m.filter != "" {
		return fmt.Sprintf("No files match %q\nPress c to clear the filter", m.filter)
	}
	if !m.showHidden {
		return "No visible files in this folder\nPress . to show hidden files"
	}
	return "This folder is empty"
}

// formatEntryLine formats a single file entry for display.
func (m *FileBrowserModel) formatEntryLine(index int, entry FileEntry) string {
	isSelected := index == m.selectedIndex
	isMultiSelected := m.selectedFiles[entry.Path]

	// Styles
	selectedStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorSecondary).
		Background(ColorBorder)
	normalStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	dirStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	fileStyle := lipgloss.NewStyle().
		Foreground(ColorText)
	sizeStyle := lipgloss.NewStyle().
		Foreground(ColorMuted)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorDim)
	hiddenStyle := lipgloss.NewStyle().
		Foreground(ColorDim).
		Italic(true)

	// Selection indicator
	prefix := "  "
	if isSelected {
		prefix = "> "
	}
	if isMultiSelected {
		prefix = "* "
	}

	// Icon
	var icon string
	var nameStyle lipgloss.Style
	if entry.IsDir {
		icon = "▸"
		nameStyle = dirStyle
	} else {
		icon = "·"
		nameStyle = fileStyle
	}

	if entry.IsHidden {
		nameStyle = hiddenStyle
	}

	// Format line
	name := safeKeyEntryText(entry.Name)
	if name == "" {
		name = "(unnamed)"
	}

	var line string
	if entry.Name == ".." {
		line = fmt.Sprintf("%s%s %s", prefix, icon, nameStyle.Render(name))
	} else if entry.IsDir {
		name = ansi.Truncate(name, max(m.viewport.Width-lipgloss.Width(prefix+icon+" ")-1, 1), "…")
		line = fmt.Sprintf("%s%s %s", prefix, icon, nameStyle.Render(name))
	} else {
		sizeStr := m.formatSize(entry.Size)
		fixed := prefix + icon + " "
		sizeText := fmt.Sprintf(" %8s", sizeStr)
		timeText := "  " + safeKeyEntryText(entry.ModTime)
		available := max(m.viewport.Width-lipgloss.Width(fixed), 1)
		if available-lipgloss.Width(sizeText+timeText) < 12 {
			timeText = ""
		}
		if available-lipgloss.Width(sizeText) < 4 {
			sizeText = ""
		}
		nameBudget := max(available-lipgloss.Width(sizeText+timeText)-1, 1)
		name = truncateLeftForWidth(name, nameBudget)
		line = fixed + nameStyle.Width(nameBudget).Render(name) +
			sizeStyle.Render(sizeText) + timeStyle.Render(timeText)
	}
	line = ansi.Truncate(line, max(m.viewport.Width, 1), "…")

	if isSelected {
		return selectedStyle.Render(line)
	}
	return normalStyle.Render(line)
}

// formatSize formats a file size for display.
func (m *FileBrowserModel) formatSize(size int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)

	switch {
	case size >= GB:
		return fmt.Sprintf("%.1fG", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.1fM", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.1fK", float64(size)/KB)
	default:
		return fmt.Sprintf("%dB", size)
	}
}

// Init initializes the file browser model.
func (m FileBrowserModel) Init() tea.Cmd {
	return nil
}

// Update handles input events for the file browser.
func (m FileBrowserModel) Update(msg tea.Msg) (FileBrowserModel, tea.Cmd) {
	var cmd tea.Cmd

	// Clear error message on any key press (user acknowledgment)
	if _, ok := msg.(tea.KeyMsg); ok && m.errorMsg != "" {
		m.errorMsg = ""
		m.updateLayout()
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// If filter is active, handle text input
		if m.filterActive {
			switch msg.Type {
			case tea.KeyEnter, tea.KeyEsc:
				m.filterActive = false
				m.updateLayout()
				m.updateViewport()
				return m, nil
			case tea.KeyBackspace:
				if runes := []rune(m.filterInput); len(runes) > 0 {
					m.filterInput = string(runes[:len(runes)-1])
					m.filter = m.filterInput
					if err := m.loadEntries(); err != nil {
						m.setError("Cannot update filter", err)
					}
				}
				return m, nil
			case tea.KeySpace:
				m.appendFilterText(" ")
				return m, nil
			default:
				if msg.Type == tea.KeyRunes {
					m.appendFilterText(string(msg.Runes))
				}
				return m, nil
			}
		}

		switch msg.String() {
		case "j", "down":
			if m.selectedIndex < len(m.entries)-1 {
				m.selectEntry(m.selectedIndex + 1)
			}

		case "k", "up":
			if m.selectedIndex > 0 {
				m.selectEntry(m.selectedIndex - 1)
			}

		case "pgup":
			m.selectEntry(m.selectedIndex - max(m.viewport.Height, 1))

		case "pgdown":
			m.selectEntry(m.selectedIndex + max(m.viewport.Height, 1))

		case "l", "enter", "right":
			if len(m.entries) > 0 && m.selectedIndex < len(m.entries) {
				entry := m.entries[m.selectedIndex]
				if entry.IsDir {
					if err := m.SetPath(entry.Path); err != nil {
						m.setError("Cannot access", err)
						return m, nil
					}
				} else {
					if m.onAction != nil {
						m.onAction(FileBrowserActionOpen, entry.Path, nil)
					}
					return m, func() tea.Msg {
						return FileBrowserActionMsg{
							Action: FileBrowserActionOpen,
							Path:   entry.Path,
						}
					}
				}
			}

		case "h", "backspace", "left":
			// Go to parent directory - show error if navigation fails
			if m.currentDir != "/" {
				if err := m.SetPath(filepath.Dir(m.currentDir)); err != nil {
					m.setError("Cannot access parent", err)
					return m, nil
				}
			}

		case " ":
			// Toggle selection
			if len(m.entries) > 0 && m.selectedIndex < len(m.entries) {
				entry := m.entries[m.selectedIndex]
				if entry.Name != ".." {
					if m.selectedFiles[entry.Path] {
						delete(m.selectedFiles, entry.Path)
					} else {
						m.selectedFiles[entry.Path] = true
					}
					m.updateLayout()
					m.updateViewport()
				}
			}

		case "/":
			// Start filtering, preserving the current query for editing.
			m.filterActive = true
			m.filterInput = m.filter
			m.updateLayout()
			m.updateViewport()

		case ".":
			// Toggle hidden files
			m.showHidden = !m.showHidden
			if err := m.loadEntries(); err != nil {
				m.showHidden = !m.showHidden
				m.setError("Cannot refresh folder", err)
			}

		case "r":
			// Refresh both directory metadata and the selected preview in place.
			// loadEntries preserves the selected path and syncSelection reloads
			// the preview, so a transient read error can recover without closing.
			if err := m.loadEntries(); err != nil {
				m.setError("Cannot refresh folder", err)
			}

		case "p":
			// Toggle preview panel
			if !m.previewEnabled && !m.previewSupported() {
				m.setError(fmt.Sprintf("Preview needs at least %d columns", minFileBrowserSplitWidth), nil)
				return m, nil
			}
			m.previewEnabled = !m.previewEnabled
			m.updateLayout()
			m.updateViewport()
			if m.previewEnabled && len(m.entries) > 0 && m.selectedIndex < len(m.entries) {
				m.loadPreview(m.entries[m.selectedIndex])
			}

		case "ctrl+j":
			// Scroll preview down
			if m.previewEnabled {
				m.previewViewport.ScrollDown(3)
			}

		case "ctrl+k":
			// Scroll preview up
			if m.previewEnabled {
				m.previewViewport.ScrollUp(3)
			}

		case "g", "home":
			m.selectEntry(0)

		case "G", "end":
			m.selectEntry(len(m.entries) - 1)

		case "~":
			// Go to home directory
			home, err := os.UserHomeDir()
			if err != nil {
				m.setError("Cannot find home folder", err)
				return m, nil
			}
			if err := m.SetPath(home); err != nil {
				m.setError("Cannot access home", err)
				return m, nil
			}

		case "y":
			// Confirm selection
			if len(m.selectedFiles) > 0 {
				var files []string
				for path := range m.selectedFiles {
					files = append(files, path)
				}
				sort.Strings(files)
				if m.onAction != nil {
					m.onAction(FileBrowserActionSelect, "", files)
				}
				return m, func() tea.Msg {
					return FileBrowserActionMsg{
						Action: FileBrowserActionSelect,
						Files:  files,
					}
				}
			}

		case "q", "esc":
			if m.onAction != nil {
				m.onAction(FileBrowserActionClose, "", nil)
			}
			return m, func() tea.Msg {
				return FileBrowserActionMsg{Action: FileBrowserActionClose}
			}

		case "c":
			// Clear filter
			m.filter = ""
			m.filterInput = ""
			if err := m.loadEntries(); err != nil {
				m.setError("Cannot clear filter", err)
			}
		}

	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	}

	return m, nil
}

func (m *FileBrowserModel) appendFilterText(value string) {
	value = sanitizeFileBrowserFilter(value)
	if value == "" {
		return
	}
	m.filterInput += value
	m.filter = m.filterInput
	if err := m.loadEntries(); err != nil {
		m.setError("Cannot update filter", err)
	}
}

func sanitizeFileBrowserFilter(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func renderFileBrowserFilterLine(filter string, active bool, width int) string {
	filter = sanitizeFileBrowserFilter(filter)
	if active {
		filter += "▊"
	}
	prefix := "  Filter: "
	if width <= lipgloss.Width(prefix) {
		return truncateTailForWidth(prefix+filter, width)
	}
	return prefix + truncateTailForWidth(filter, width-lipgloss.Width(prefix))
}

func truncateTailForWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width == 1 {
		runes := []rune(value)
		if len(runes) > 0 && lipgloss.Width(string(runes[len(runes)-1])) == 1 {
			return string(runes[len(runes)-1])
		}
		return "…"
	}

	runes := []rune(value)
	budget := width - 1
	used := 0
	start := len(runes)
	for start > 0 {
		cellWidth := lipgloss.Width(string(runes[start-1]))
		if used+cellWidth > budget {
			break
		}
		start--
		used += cellWidth
	}
	return "…" + string(runes[start:])
}

func (m *FileBrowserModel) setError(context string, err error) {
	m.errorMsg = safeKeyEntryText(context)
	if err != nil {
		m.errorMsg += ": " + safeKeyEntryText(err.Error())
	}
	m.updateLayout()
}

// View renders the file browser.
func (m FileBrowserModel) View() string {
	var builder strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Padding(0, 1)

	builder.WriteString(headerStyle.Render("Files"))
	builder.WriteString("\n\n")

	// Current path
	pathStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	pathWidth := max(m.renderWidth()-4, 1)
	displayDir := safeKeyEntryText(m.currentDir)
	fmt.Fprintf(&builder, "  %s\n", pathStyle.Render(truncateForWidth(displayDir, pathWidth)))

	// Selection count
	if len(m.selectedFiles) > 0 {
		selectStyle := lipgloss.NewStyle().
			Foreground(ColorSuccess)
		builder.WriteString(selectStyle.Render(fmt.Sprintf("  %d selected\n", len(m.selectedFiles))))
	}

	// Filter indicator
	if m.filter != "" || m.filterActive {
		filterStyle := lipgloss.NewStyle().
			Foreground(ColorWarning)
		filterLine := renderFileBrowserFilterLine(m.filter, m.filterActive, m.renderWidth())
		builder.WriteString(filterStyle.Render(filterLine))
		builder.WriteString("\n")
	}

	// Error message (if any)
	if m.errorMsg != "" {
		errorStyle := lipgloss.NewStyle().
			Foreground(ColorError).
			Bold(true)
		errorText := ansi.Truncate(safeKeyEntryText(m.errorMsg), max(m.renderWidth()-4, 1), "…")
		builder.WriteString(errorStyle.Render(fmt.Sprintf("  ⚠ %s\n", errorText)))
	}

	builder.WriteString("\n")

	// Content area - split view or single view
	if m.previewEnabled && (m.width == 0 || m.width >= minFileBrowserSplitWidth) {
		builder.WriteString(m.renderSplitView())
	} else {
		borderStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1)
		builder.WriteString(borderStyle.Width(m.viewport.Width).Render(m.viewport.View()))
	}
	builder.WriteString("\n\n")

	// Footer with actions
	m.renderActions(&builder)

	return builder.String()
}

// renderSplitView renders the file browser with a preview panel.
func (m FileBrowserModel) renderSplitView() string {
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	// Left panel: file list
	leftPanel := borderStyle.Width(m.listWidth).Render(m.viewport.View())

	// Right panel: preview
	var previewContent string
	if m.previewLoadError != "" {
		errorStyle := lipgloss.NewStyle().Foreground(ColorWarning).Italic(true)
		recoveryStyle := lipgloss.NewStyle().Foreground(ColorAccent)
		previewContent = errorStyle.Render(safeKeyEntryText(m.previewLoadError))
		if recovery := safeKeyEntryText(m.previewRecovery); recovery != "" {
			previewContent += "\n" + recoveryStyle.Render(recovery)
		}
	} else if m.previewContent != "" {
		previewContent = m.previewViewport.View()
	} else {
		dimStyle := lipgloss.NewStyle().
			Foreground(ColorDim).
			Italic(true)
		previewContent = dimStyle.Render("Select a file to preview")
		if m.selectedIndex >= 0 && m.selectedIndex < len(m.entries) && m.entries[m.selectedIndex].IsDir {
			previewContent = dimStyle.Render("Folder selected · Enter to open")
		}
	}

	// Preview header
	previewHeader := ""
	if m.previewFilePath != "" {
		headerStyle := lipgloss.NewStyle().
			Foreground(ColorHighlight).
			Bold(true)
		fileName := filepath.Base(safeKeyEntryText(m.previewFilePath))
		previewHeader = headerStyle.Render(truncateForWidth(fileName, m.previewWidth)) + "\n" + strings.Repeat("─", m.previewWidth) + "\n"
	}

	rightPanel := borderStyle.Width(m.previewWidth).Render(previewHeader + previewContent)

	// Join horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, " ", rightPanel)
}

// renderActions renders the available actions.
func (m *FileBrowserModel) renderActions(builder *strings.Builder) {
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Bold(true)

	if m.filterActive {
		hints := []string{
			keyStyle.Render("Enter/Esc") + " Done",
			keyStyle.Render("Type") + " Filter",
			keyStyle.Render("Backspace") + " Delete",
		}
		builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(hints, "  │  ")), m.renderWidth(), "…"))
		return
	}

	// Escape is the guaranteed recovery path from this full-screen overlay.
	// Keep it first: the line is truncated on narrow terminals, so trailing
	// actions may disappear but closing the panel must remain discoverable.
	hints := []string{keyStyle.Render("Esc/q") + " Close"}
	if m.selectedIndex >= 0 && m.selectedIndex < len(m.entries) {
		entryAction := "Open folder"
		if !m.entries[m.selectedIndex].IsDir {
			entryAction = "Add to draft"
		}
		hints = append(hints, keyStyle.Render("Enter")+" "+entryAction)
	}
	hints = append(hints, keyStyle.Render("/")+" Filter")
	if m.previewSupported() || m.previewEnabled {
		previewAction := "Preview"
		if m.previewEnabled {
			previewAction = "Hide preview"
		}
		hints = append(hints, keyStyle.Render("p")+" "+previewAction)
	}
	if m.selectedIndex >= 0 && m.selectedIndex < len(m.entries) && m.entries[m.selectedIndex].Name != ".." {
		hints = append(hints, keyStyle.Render("Space")+" Select")
	}
	if m.filter != "" {
		hints = append(hints, keyStyle.Render("c")+" Clear")
	}
	hiddenAction := "Show hidden"
	if m.showHidden {
		hiddenAction = "Hide hidden"
	}
	hints = append(hints,
		keyStyle.Render(".")+" "+hiddenAction,
	)

	builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(hints, "  ")), m.renderWidth(), "…"))
	builder.WriteString("\n")
	secondaryHints := make([]string, 0, 7)
	if len(m.selectedFiles) > 0 {
		secondaryHints = append(secondaryHints, "y: Add selection to draft")
	}
	secondaryHints = append(secondaryHints, "r Refresh")
	if !m.previewSupported() {
		secondaryHints = append(secondaryHints, fmt.Sprintf("Preview needs %d columns", minFileBrowserSplitWidth))
	}
	secondaryHints = append(secondaryHints, "↑/↓ Move", "PgUp/PgDn Page", "Home/End Jump", "h/l Folder")
	if m.previewEnabled {
		secondaryHints = append(secondaryHints, "Ctrl+j/k Scroll preview")
	} else {
		secondaryHints = append(secondaryHints, "~ Home folder")
	}
	builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(secondaryHints, "  │  ")), m.renderWidth(), "…"))
}

// GetCurrentPath returns the current directory path.
func (m FileBrowserModel) GetCurrentPath() string {
	return m.currentDir
}

// GetSelectedFiles returns the selected files.
func (m FileBrowserModel) GetSelectedFiles() []string {
	var files []string
	for path := range m.selectedFiles {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}
