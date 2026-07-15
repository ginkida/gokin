package ui

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sergi/go-diff/diffmatchpatch"

	"gokin/internal/highlight"
)

// diffSyntaxHighlighter is a package-level chroma-backed highlighter
// shared across all DiffPreviewModel instances. Constructing one per
// model would load lexer metadata on every approval prompt; shared
// instance is safe because chroma lexers are stateless.
var diffSyntaxHighlighter = highlight.New("monokai")

// DiffDecision represents user's decision on a diff preview.
type DiffDecision int

const (
	DiffPending DiffDecision = iota
	DiffApply
	DiffReject
	DiffEdit
	DiffApplyAll
	DiffRejectAll
)

// DiffPreviewModel is the UI component for displaying diff previews.
type DiffPreviewModel struct {
	viewport          viewport.Model
	diff              string
	filePath          string
	oldContent        string
	newContent        string
	displayOldContent string
	displayNewContent string
	decision          DiffDecision
	toolName          string
	isNewFile         bool
	styles            *Styles
	width             int
	height            int

	// Configurable context lines (default 3)
	contextLines int

	// Ignore whitespace-only changes
	ignoreWhitespace    bool
	sideBySide          bool
	changeOffsets       []int
	currentChange       int
	responseUnavailable bool
	responsePending     bool // a decision command is already on its way to the parent
	requestID           string
	terminalSizeKnown   bool

	// Callback when user makes a decision
	onDecision func(decision DiffDecision)
}

// DiffPreviewRequestMsg is sent to request a diff preview.
type DiffPreviewRequestMsg struct {
	RequestID  string
	FilePath   string
	OldContent string
	NewContent string
	ToolName   string
	IsNewFile  bool
}

// DiffPreviewResponseMsg is sent when user makes a decision.
type DiffPreviewResponseMsg struct {
	RequestID  string
	Decision   DiffDecision
	FilePath   string
	NewContent string
	triggerKey string
}

// NewDiffPreviewModel creates a new diff preview model.
func NewDiffPreviewModel(styles *Styles) DiffPreviewModel {
	vp := viewport.New(80, 20)
	vp.MouseWheelEnabled = true

	return DiffPreviewModel{
		viewport:     vp,
		styles:       styles,
		decision:     DiffPending,
		contextLines: 3,
		sideBySide:   true,
	}
}

// SetSize sets the size of the diff preview. Also triggers a re-render if
// content is already loaded — a resize that crosses minSideBySideWidth must
// flip between split and unified layouts (Sprint UI polish: auto-fallback
// on narrow terminals relies on this re-render).
func (m *DiffPreviewModel) SetSize(width, height int) {
	m.terminalSizeKnown = width > 0 && height > 0
	m.width = max(width, 1)
	m.height = max(height, 1)
	m.viewport.Width = max(m.width-4, 1)
	m.viewport.Height = max(m.height-10, 1) // Reserve space for header and footer

	// Re-render only when content has already been loaded. Before SetContent
	// there's nothing to lay out, and refreshDiffView would just produce
	// empty-diff output we'd immediately overwrite.
	if m.oldContent != "" || m.newContent != "" {
		m.refreshDiffView()
	}
}

func (m DiffPreviewModel) renderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return max(m.viewport.Width, 1)
}

// SetContent sets the diff content to display.
func (m *DiffPreviewModel) SetContent(filePath, oldContent, newContent, toolName string, isNewFile bool) {
	m.filePath = filePath
	m.oldContent = oldContent
	m.newContent = newContent
	m.displayOldContent = visibleTerminalControlText(oldContent)
	m.displayNewContent = visibleTerminalControlText(newContent)
	m.toolName = toolName
	m.isNewFile = isNewFile
	m.decision = DiffPending
	m.responseUnavailable = false
	m.responsePending = false

	m.refreshDiffView()
}

func (m *DiffPreviewModel) SetRequestID(requestID string) {
	m.requestID = requestID
}

// minSideBySideWidth is the viewport width below which side-by-side mode
// becomes unreadable (each column gets squished to the 20-char floor and
// truncation eats context). Falls back to unified diff automatically —
// users on narrow terminals (SSH, tmux splits) still see usable output.
const minSideBySideWidth = 80

// refreshDiffView regenerates and re-renders the diff with current settings.
func (m *DiffPreviewModel) refreshDiffView() {
	m.diff = m.generateDiff(m.displayOldContent, m.displayNewContent)
	var content string
	if m.effectiveSideBySide() {
		content = m.renderSideBySide()
		m.changeOffsets = m.detectChangeOffsets(content)
	} else {
		content = m.highlightDiff(m.diff)
		m.changeOffsets = m.detectChangeOffsets(m.diff)
	}
	if len(m.changeOffsets) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		message := "No content changes detected."
		if m.ignoreWhitespace && m.displayOldContent != m.displayNewContent {
			message = "All changes are whitespace-only and currently hidden.\nPress I to show them."
		}
		content = emptyStyle.Render(message)
	}
	m.viewport.SetContent(content)
	m.currentChange = -1
	m.viewport.GotoTop()
}

// effectiveSideBySide reports whether the current viewport is actually
// showing split view — respects both the user toggle AND the narrow-
// terminal auto-fallback. Used by the status footer so its "view: split"
// hint doesn't lie when we've downgraded.
func (m DiffPreviewModel) effectiveSideBySide() bool {
	return m.sideBySide && !m.ignoreWhitespace && m.viewport.Width >= minSideBySideWidth
}

// SetDecisionCallback sets the callback for when user makes a decision.
func (m *DiffPreviewModel) SetDecisionCallback(callback func(DiffDecision)) {
	m.onDecision = callback
}

func (m *DiffPreviewModel) MarkResponseUnavailable() {
	m.responseUnavailable = true
	m.responsePending = false
	m.decision = DiffPending
}

// diffLine represents a single line in the diff with its type.
type diffLine struct {
	Type diffmatchpatch.Operation
	Text string
}

// generateDiff creates a unified diff between old and new content.
func (m *DiffPreviewModel) generateDiff(oldContent, newContent string) string {
	dmp := diffmatchpatch.New()

	var result strings.Builder

	// Header
	displayPath := safeKeyEntryText(m.filePath)
	fmt.Fprintf(&result, "--- %s\n", displayPath)
	fmt.Fprintf(&result, "+++ %s\n", displayPath)

	// Generate line-based diff
	diffs := dmp.DiffMain(oldContent, newContent, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	// Convert diffs to individual lines with their types
	var allLines []diffLine
	for _, d := range diffs {
		lines := strings.Split(d.Text, "\n")
		for i, line := range lines {
			if i == len(lines)-1 && line == "" {
				continue
			}
			allLines = append(allLines, diffLine{Type: d.Type, Text: line})
		}
	}

	// Build hunks with context lines
	contextN := m.contextLines
	if contextN < 0 {
		contextN = 0
	}

	// Find ranges of changed lines (non-Equal)
	type changeRange struct {
		start, end int // indices into allLines
	}
	var changes []changeRange
	i := 0
	for i < len(allLines) {
		if allLines[i].Type != diffmatchpatch.DiffEqual {
			start := i
			for i < len(allLines) && allLines[i].Type != diffmatchpatch.DiffEqual {
				i++
			}
			changes = append(changes, changeRange{start, i})
		} else {
			i++
		}
	}

	if len(changes) == 0 {
		return result.String()
	}

	// Merge change ranges that are close together (separated by <= 2*contextN equal lines)
	type hunkRange struct {
		start, end int // indices into allLines, including context
	}
	var hunks []hunkRange

	for _, ch := range changes {
		hStart := ch.start - contextN
		if hStart < 0 {
			hStart = 0
		}
		hEnd := ch.end + contextN
		if hEnd > len(allLines) {
			hEnd = len(allLines)
		}

		if len(hunks) > 0 && hStart <= hunks[len(hunks)-1].end {
			// Merge with previous hunk
			hunks[len(hunks)-1].end = hEnd
		} else {
			hunks = append(hunks, hunkRange{hStart, hEnd})
		}
	}

	// Compute old/new line numbers for each position in allLines
	oldLineNums := make([]int, len(allLines)+1)
	newLineNums := make([]int, len(allLines)+1)
	oldLine := 1
	newLine := 1
	for idx, dl := range allLines {
		oldLineNums[idx] = oldLine
		newLineNums[idx] = newLine
		switch dl.Type {
		case diffmatchpatch.DiffEqual:
			oldLine++
			newLine++
		case diffmatchpatch.DiffDelete:
			oldLine++
		case diffmatchpatch.DiffInsert:
			newLine++
		}
	}
	oldLineNums[len(allLines)] = oldLine
	newLineNums[len(allLines)] = newLine

	// Render each hunk
	for _, hunk := range hunks {
		// Calculate hunk header line counts
		oldStart := oldLineNums[hunk.start]
		newStart := newLineNums[hunk.start]
		oldCount := 0
		newCount := 0
		for idx := hunk.start; idx < hunk.end; idx++ {
			switch allLines[idx].Type {
			case diffmatchpatch.DiffEqual:
				oldCount++
				newCount++
			case diffmatchpatch.DiffDelete:
				oldCount++
			case diffmatchpatch.DiffInsert:
				newCount++
			}
		}

		// Check if this hunk is whitespace-only when ignoreWhitespace is enabled
		if m.ignoreWhitespace && isWhitespaceOnlyHunk(allLines[hunk.start:hunk.end]) {
			continue
		}

		// Find the nearest function/class/def line above the hunk start
		funcName := findNearestFuncName(allLines, hunk.start)

		// Write hunk header
		header := fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)
		if funcName != "" {
			header += " " + funcName
		}
		result.WriteString(header + "\n")

		// Write hunk lines
		for idx := hunk.start; idx < hunk.end; idx++ {
			dl := allLines[idx]
			switch dl.Type {
			case diffmatchpatch.DiffEqual:
				fmt.Fprintf(&result, " %s\n", dl.Text)
			case diffmatchpatch.DiffDelete:
				fmt.Fprintf(&result, "-%s\n", dl.Text)
			case diffmatchpatch.DiffInsert:
				fmt.Fprintf(&result, "+%s\n", dl.Text)
			}
		}
	}

	return result.String()
}

// findNearestFuncName searches backward from the given position to find the nearest
// function, class, or def declaration in the context (equal) lines.
func findNearestFuncName(lines []diffLine, startIdx int) string {
	for i := startIdx - 1; i >= 0; i-- {
		text := lines[i].Text
		// Only consider equal (context) lines for function detection
		if lines[i].Type != diffmatchpatch.DiffEqual {
			continue
		}
		trimmed := strings.TrimSpace(text)
		for _, prefix := range []string{"func ", "class ", "def "} {
			if strings.HasPrefix(trimmed, prefix) {
				// Return the full signature up to the opening brace or end of line
				sig := trimmed
				if braceIdx := strings.Index(sig, "{"); braceIdx > 0 {
					sig = strings.TrimSpace(sig[:braceIdx])
				}
				return sig
			}
		}
	}
	return ""
}

// isWhitespaceOnlyHunk checks if a hunk contains only whitespace changes.
func isWhitespaceOnlyHunk(lines []diffLine) bool {
	var removed, added []string
	for _, dl := range lines {
		normalized := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, dl.Text)
		switch dl.Type {
		case diffmatchpatch.DiffDelete:
			removed = append(removed, normalized)
		case diffmatchpatch.DiffInsert:
			added = append(added, normalized)
		}
	}
	// Ignore every whitespace rune, including line boundaries. This matches
	// the user-facing "ignore whitespace" contract and handles the diff
	// library splitting indentation and trailing spaces into separate chunks.
	return len(removed)+len(added) > 0 && strings.Join(removed, "") == strings.Join(added, "")
}

// highlightDiff applies syntax highlighting to the diff with inline word-level highlighting.
func (m *DiffPreviewModel) highlightDiff(diff string) string {
	addedStyle := lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
	removedStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	addedWordStyle := lipgloss.NewStyle().Foreground(ColorText).Background(ColorSuccess).Bold(true)
	removedWordStyle := lipgloss.NewStyle().Foreground(ColorText).Background(ColorError).Bold(true)
	headerStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
	hunkStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	contextStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	lines := strings.Split(diff, "\n")
	var result strings.Builder

	// Pre-scan to pair consecutive removed/added line blocks for word-level diff
	paired := make(map[int]int) // maps removed line index -> added line index and vice versa

	i := 0
	for i < len(lines) {
		// Find a block of removed lines followed by added lines
		if strings.HasPrefix(lines[i], "-") && !strings.HasPrefix(lines[i], "---") {
			removeStart := i
			for i < len(lines) && strings.HasPrefix(lines[i], "-") && !strings.HasPrefix(lines[i], "---") {
				i++
			}
			removeEnd := i

			addStart := i
			for i < len(lines) && strings.HasPrefix(lines[i], "+") && !strings.HasPrefix(lines[i], "+++") {
				i++
			}
			addEnd := i

			// Pair up removed and added lines 1-to-1
			removeCount := removeEnd - removeStart
			addCount := addEnd - addStart
			pairCount := removeCount
			if addCount < pairCount {
				pairCount = addCount
			}
			for p := 0; p < pairCount; p++ {
				paired[removeStart+p] = addStart + p
				paired[addStart+p] = removeStart + p
			}
		} else {
			i++
		}
	}

	// Detect language from filename so chroma picks the right lexer
	// for CONTEXT lines. Returns "" (auto-detect) for unknown extensions —
	// chroma falls back to a generic lexer in that case. We leave +/-
	// lines un-highlighted because ANSI-on-ANSI (chroma colors inside
	// our green/red markers) collides visually; the word-diff highlight
	// already carries enough signal for edited lines.
	lang := diffSyntaxHighlighter.DetectLanguage(m.filePath)

	for idx, line := range lines {
		var styledLine string

		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			styledLine = headerStyle.Render(line)
		case strings.HasPrefix(line, "@@"):
			styledLine = hunkStyle.Render(line)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			if partnerIdx, ok := paired[idx]; ok && strings.HasPrefix(lines[partnerIdx], "+") {
				// Word-level highlight for paired removed line
				oldText := line[1:] // strip the "-" prefix
				newText := lines[partnerIdx][1:]
				styledLine = removedStyle.Render("-") + highlightWordDiffs(oldText, newText, removedStyle, removedWordStyle)
			} else {
				styledLine = removedStyle.Render(line)
			}
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			if partnerIdx, ok := paired[idx]; ok && strings.HasPrefix(lines[partnerIdx], "-") {
				// Word-level highlight for paired added line
				oldText := lines[partnerIdx][1:]
				newText := line[1:]
				styledLine = addedStyle.Render("+") + highlightWordDiffs(newText, oldText, addedStyle, addedWordStyle)
			} else {
				styledLine = addedStyle.Render(line)
			}
		default:
			// Context line (space prefix or no prefix). Apply chroma
			// syntax highlighting to the code content so the user sees
			// surrounding keywords/strings/comments in their usual
			// colors — matching Claude Code's diff feel. Falls back to
			// gray contextStyle when chroma returns empty (unknown lang).
			styledLine = renderContextLineSyntax(line, lang, contextStyle)
		}

		result.WriteString(styledLine)
		result.WriteString("\n")
	}

	return result.String()
}

// renderContextLineSyntax applies chroma syntax highlighting to the
// code portion of a unified-diff context line. Strips a single leading
// " " (the unified-diff context marker) before lexing, then re-prepends
// it with the dim context color so the column alignment stays with
// +/- lines.
//
// Safety:
//   - Empty or whitespace-only content bypasses chroma (no tokens to
//     lex, and chroma can return stray terminators that look like
//     phantom styling).
//   - If chroma emits an ANSI reset mid-line that matches our context
//     style, the renderer already closes any leftover escape sequences.
func renderContextLineSyntax(line, lang string, contextStyle lipgloss.Style) string {
	if line == "" {
		return ""
	}
	prefix := ""
	code := line
	if strings.HasPrefix(line, " ") {
		prefix = " "
		code = line[1:]
	}
	if strings.TrimSpace(code) == "" {
		return contextStyle.Render(line)
	}
	highlighted := diffSyntaxHighlighter.Highlight(code, lang)
	if strings.TrimSpace(highlighted) == "" {
		// chroma returned empty / whitespace — fall back to unified
		// gray styling so the line still renders distinctly.
		return contextStyle.Render(line)
	}
	// Chroma output already ends with a reset; add the leading column
	// marker in the context color so indentation lines up with +/-.
	return contextStyle.Render(prefix) + strings.TrimRight(highlighted, "\n")
}

// highlightWordDiffs highlights the words in 'text' that differ from 'other'.
// baseStyle is applied to unchanged portions; emphStyle is applied to changed words.
// 'text' is the line we are rendering; 'other' is the counterpart line for comparison.
func highlightWordDiffs(text, other string, baseStyle, emphStyle lipgloss.Style) string {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(other, text, false)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var result strings.Builder
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			result.WriteString(baseStyle.Render(d.Text))
		case diffmatchpatch.DiffInsert:
			// This text is unique to 'text' (the line we are rendering)
			result.WriteString(emphStyle.Render(d.Text))
		case diffmatchpatch.DiffDelete:
			// This text is unique to 'other' — skip it for this line's rendering
		}
	}
	return result.String()
}

// Init initializes the diff preview model.
func (m DiffPreviewModel) Init() tea.Cmd {
	return nil
}

// Update handles input events for the diff preview.
func (m DiffPreviewModel) Update(msg tea.Msg) (DiffPreviewModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.responsePending {
			switch msg.String() {
			case "y", "Y", "n", "N", "esc", "A", "R":
				// Bubble Tea commands are asynchronous. Ignore a second decision
				// until the parent consumes the first one, otherwise a rapid double
				// press can invoke the backend twice and later clobber a newer modal.
				return m, nil
			}
		}
		switch msg.String() {
		case "y", "Y":
			if m.responseUnavailable || m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
				return m, nil
			}
			m.decision = DiffApply
			m.responsePending = true
			if m.onDecision != nil {
				m.onDecision(DiffApply)
			}
			triggerKey := msg.String()
			return m, func() tea.Msg {
				return DiffPreviewResponseMsg{
					RequestID:  m.requestID,
					Decision:   DiffApply,
					FilePath:   m.filePath,
					NewContent: m.newContent,
					triggerKey: triggerKey,
				}
			}

		case "n", "N", "esc":
			m.decision = DiffReject
			m.responsePending = true
			if m.onDecision != nil {
				m.onDecision(DiffReject)
			}
			triggerKey := msg.String()
			return m, func() tea.Msg {
				return DiffPreviewResponseMsg{
					RequestID:  m.requestID,
					Decision:   DiffReject,
					FilePath:   m.filePath,
					NewContent: m.newContent,
					triggerKey: triggerKey,
				}
			}
		case "A":
			if m.responseUnavailable || m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
				return m, nil
			}
			m.decision = DiffApplyAll
			m.responsePending = true
			if m.onDecision != nil {
				m.onDecision(DiffApplyAll)
			}
			triggerKey := msg.String()
			return m, func() tea.Msg {
				return DiffPreviewResponseMsg{
					RequestID:  m.requestID,
					Decision:   DiffApplyAll,
					FilePath:   m.filePath,
					NewContent: m.newContent,
					triggerKey: triggerKey,
				}
			}

		case "R":
			m.decision = DiffRejectAll
			m.responsePending = true
			if m.onDecision != nil {
				m.onDecision(DiffRejectAll)
			}
			triggerKey := msg.String()
			return m, func() tea.Msg {
				return DiffPreviewResponseMsg{
					RequestID:  m.requestID,
					Decision:   DiffRejectAll,
					FilePath:   m.filePath,
					NewContent: m.newContent,
					triggerKey: triggerKey,
				}
			}

		case "j", "down":
			m.viewport, cmd = m.viewport.Update(tea.KeyMsg{Type: tea.KeyDown})
			return m, cmd

		case "k", "up":
			m.viewport, cmd = m.viewport.Update(tea.KeyMsg{Type: tea.KeyUp})
			return m, cmd

		case "g":
			m.viewport.GotoTop()
			return m, nil

		case "G":
			m.viewport.GotoBottom()
			return m, nil

		case "ctrl+d":
			m.viewport.HalfPageDown()
			return m, nil

		case "ctrl+u":
			m.viewport.HalfPageUp()
			return m, nil

		case "=": // "+" key (shift not needed on most keyboards, use "=" as alias for "+")
			m.contextLines++
			if m.contextLines > 20 {
				m.contextLines = 20
			}
			m.refreshDiffView()
			return m, nil

		case "-":
			m.contextLines--
			if m.contextLines < 0 {
				m.contextLines = 0
			}
			m.refreshDiffView()
			return m, nil

		case "I":
			m.ignoreWhitespace = !m.ignoreWhitespace
			m.refreshDiffView()
			return m, nil
		case "s":
			m.sideBySide = !m.sideBySide
			m.refreshDiffView()
			return m, nil
		case "]":
			m.jumpToChange(true)
			return m, nil
		case "[":
			m.jumpToChange(false)
			return m, nil
		}

	case tea.MouseMsg:
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil
	}

	return m, nil
}

// View renders the diff preview (Claude Code style — no bordered boxes).
func (m DiffPreviewModel) View() string {
	// The app frame owns a final status row and the join row below this view.
	// At tiny heights the ordinary header/viewport/four-line footer is cropped
	// from the top, leaving Apply/Reject visible without the file they affect.
	// Render an explicit review summary whose identity and primary decisions fit
	// inside the remaining rows.
	if m.height > 0 && m.height <= 8 {
		return m.renderTinyView()
	}

	var builder strings.Builder

	markerStyle := lipgloss.NewStyle().Foreground(ColorDim)
	nameStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	fileStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	toolStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	// Header: ● Diff Preview
	bulletStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	titleLine := bulletStyle.Render("● ") + nameStyle.Render("Diff Preview")
	builder.WriteString(ansi.Truncate(titleLine, m.renderWidth(), "…"))
	builder.WriteString("\n")

	// File info with ⎿ marker
	var fileLabel string
	if m.isNewFile {
		fileLabel = "New file: "
	} else {
		fileLabel = "Modified: "
	}
	filePath := safeKeyEntryText(m.filePath)
	if filePath == "" {
		filePath = "(unknown file)"
	}
	toolPrefix := ""
	if toolName := safeKeyEntryText(m.toolName); toolName != "" {
		toolPrefix = toolStyle.Render(toolName + " → ")
	}
	fileLine := markerStyle.Render("  ⎿  ") + toolPrefix + fileStyle.Render(fileLabel+filePath)
	builder.WriteString(ansi.Truncate(fileLine, m.renderWidth(), "…"))
	builder.WriteString("\n")

	// Diff statistics and settings
	addCount, removeCount := m.countChanges()
	addedStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	removedStyle := lipgloss.NewStyle().Foreground(ColorError)
	settingsStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	stats := fmt.Sprintf("%s, %s",
		addedStyle.Render(fmt.Sprintf("+%d", addCount)),
		removedStyle.Render(fmt.Sprintf("-%d", removeCount)))
	wsLabel := "off"
	if m.ignoreWhitespace {
		wsLabel = "on"
	}
	settings := settingsStyle.Render(fmt.Sprintf("  context: %d | ignore-ws: %s", m.contextLines, wsLabel))
	viewMode := "unified"
	if m.effectiveSideBySide() {
		viewMode = "split"
	} else if m.sideBySide && m.ignoreWhitespace {
		viewMode = "split→unified (ignore-ws)"
	} else if m.sideBySide {
		// User wants split but the viewport is too narrow — make that
		// visible in the status line so toggling feels deterministic.
		viewMode = "split→unified (narrow)"
	}
	settings += settingsStyle.Render(" | view: " + viewMode)
	statusLine := markerStyle.Render("     ") + stats + settings
	builder.WriteString(ansi.Truncate(statusLine, m.renderWidth(), "…"))
	builder.WriteString("\n\n")

	// Diff viewport without border
	builder.WriteString(m.viewport.View())
	builder.WriteString("\n\n")

	// Footer with actions
	m.renderActions(&builder)

	return builder.String()
}

func (m DiffPreviewModel) renderTinyView() string {
	width := max(m.renderWidth(), 1)
	identityStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	statusStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)

	prefix := "Diff · "
	if m.isNewFile {
		prefix = "New · "
	}
	path := safeKeyEntryText(m.filePath)
	if path == "" {
		path = "(unknown file)"
	}
	identity := prefix + truncateLeftForWidth(path, max(width-lipgloss.Width(prefix), 1))
	identity = ansi.Truncate(identityStyle.Render(identity), width, "…")

	added, removed := m.countChanges()
	viewMode := "unified"
	if m.effectiveSideBySide() {
		viewMode = "split"
	}
	context := fmt.Sprintf("+%d -%d · %s", added, removed, viewMode)
	if !m.hasVisibleChanges() {
		context = "No visible changes"
	}
	context = ansi.Truncate(statusStyle.Render(context), width, "…")

	rowBudget := max(m.height-2, 1)
	if m.responseUnavailable {
		return renderTinyReviewRows(
			identity,
			ansi.Truncate(warningStyle.Render("Decision unavailable"), width, "…"),
			[]string{ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc")+" Cancel"), width, "…")},
			rowBudget,
		)
	}
	if m.responsePending {
		return renderTinyReviewRows(
			identity,
			context,
			[]string{ansi.Truncate(statusStyle.Render("Submitting decision…"), width, "…")},
			rowBudget,
		)
	}
	if m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
		resize := warningStyle.Render(tinyReviewResizeLabel(width))
		reject := keyStyle.Render("n")
		if width >= 4 {
			reject += hintStyle.Render(" No")
		}
		return renderTinyResizeRows(identity, resize, reject, rowBudget)
	}

	applyLabel, rejectLabel := "Apply", "Reject"
	if !m.hasVisibleChanges() {
		applyLabel, rejectLabel = "Continue", "Cancel"
	}
	primary := keyStyle.Render("y") + " " + applyLabel + " · " + keyStyle.Render("n") + " " + rejectLabel
	if !m.hasVisibleChanges() && width < 24 {
		primary = keyStyle.Render("y") + " Continue " + keyStyle.Render("n") + " Cancel"
	}
	recovery := keyStyle.Render("Esc") + " Reject · " + keyStyle.Render("A/R")
	return renderTinyReviewRows(
		identity,
		context,
		[]string{
			ansi.Truncate(hintStyle.Render(primary), width, "…"),
			ansi.Truncate(hintStyle.Render(recovery), width, "…"),
		},
		rowBudget,
	)
}

// renderTinyReviewRows keeps identity first and required controls last, adding
// context only when it does not displace either. This is intentionally not a
// tail slice: generic tail-cropping caused the context-free approval defect.
func renderTinyReviewRows(identity, context string, required []string, budget int) string {
	budget = max(budget, 1)
	rows := []string{identity}
	remaining := budget - 1
	if context != "" && remaining > len(required) {
		rows = append(rows, context)
		remaining--
	}
	for _, row := range required {
		if remaining <= 0 {
			break
		}
		rows = append(rows, row)
		remaining--
	}
	return strings.Join(rows, "\n")
}

// A tiny review must fail closed when neither the target identity nor the
// apply/confirm affordance can be rendered legibly. Reject/back remain usable;
// widening the terminal is the only route to an applying decision.
func tinyReviewNeedsResize(width, height int) bool {
	return width > 0 && height > 0 && height <= 8 && (width <= 7 || height <= 3)
}

func tinyReviewResizeLabel(width int) string {
	if width >= 6 {
		return "Resize"
	}
	return "↔"
}

func renderTinyResizeRows(identity, resize, safeAction string, budget int) string {
	budget = max(budget, 1)
	switch budget {
	case 1:
		return safeAction
	case 2:
		return resize + "\n" + safeAction
	default:
		return identity + "\n" + resize + "\n" + safeAction
	}
}

// countChanges counts added and removed lines.
func (m DiffPreviewModel) countChanges() (added, removed int) {
	lines := strings.Split(m.diff, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
}

func (m DiffPreviewModel) hasVisibleChanges() bool {
	added, removed := m.countChanges()
	return added+removed > 0
}

func (m DiffPreviewModel) canScrollDiff() bool {
	return m.viewport.TotalLineCount() > m.viewport.Height
}

func (m DiffPreviewModel) canJumpChanges() bool {
	return len(m.changeOffsets) > 0
}

// renderActions renders the action buttons.
func (m DiffPreviewModel) renderActions(builder *strings.Builder) {
	// Styled buttons
	applyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText).
		Background(ColorSuccess).
		Padding(0, 2)

	rejectStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText).
		Background(ColorError).
		Padding(0, 2)

	hintStyle := lipgloss.NewStyle().
		Foreground(ColorDim)
	if m.responseUnavailable {
		warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		builder.WriteString(ansi.Truncate(warningStyle.Render("Unavailable: cannot submit diff decision"), m.renderWidth(), "…"))
		builder.WriteString("\n")
		keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		builder.WriteString(ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc")+" Cancel · view controls remain available"), m.renderWidth(), "…"))
		var inspection []string
		if m.canScrollDiff() {
			inspection = append(inspection, "j/k Scroll", "g/G Top/Bottom", "Ctrl+D/U Half page")
		}
		if m.canJumpChanges() {
			inspection = append(inspection, "[ ] Changes")
		}
		inspection = append(inspection, "+/- Context", "I Whitespace", "s View")
		builder.WriteString("\n")
		builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(inspection, "  ·  ")), m.renderWidth(), "…"))
		return
	}

	applyLabel := "y Apply"
	rejectLabel := "n Reject"
	if !m.hasVisibleChanges() {
		applyLabel = "y Continue"
		rejectLabel = "n Cancel"
	}
	if m.renderWidth() < 32 {
		keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		writeLine := func(line string) {
			builder.WriteString(ansi.Truncate(hintStyle.Render(line), m.renderWidth(), "…"))
			builder.WriteString("\n")
		}
		if m.renderWidth() < 20 {
			writeLine(keyStyle.Render("y") + " Yes " + keyStyle.Render("n") + " No")
			writeLine(keyStyle.Render("A") + " All " + keyStyle.Render("R") + " No")
			var navigation []string
			if m.canScrollDiff() {
				navigation = append(navigation, "j/k Scroll")
			}
			if m.canJumpChanges() {
				navigation = append(navigation, "[ ] Changes")
			}
			if len(navigation) > 0 {
				writeLine(strings.Join(navigation, "  ·  "))
			}
			builder.WriteString(ansi.Truncate(hintStyle.Render("s View  I WS"), m.renderWidth(), "…"))
			return
		}
		writeLine(keyStyle.Render(applyLabel[:1]) + applyLabel[1:] + "  ·  " + keyStyle.Render(rejectLabel[:1]) + rejectLabel[1:])
		var navigation []string
		if m.canScrollDiff() {
			navigation = append(navigation, "j/k Scroll")
		}
		if m.canJumpChanges() {
			navigation = append(navigation, "[ ] Changes")
		}
		if len(navigation) > 0 {
			writeLine(strings.Join(navigation, "  ·  "))
		}
		writeLine("s View  ·  I Whitespace")
		builder.WriteString(ansi.Truncate(hintStyle.Render("A Apply all  ·  R Reject all"), m.renderWidth(), "…"))
		return
	}
	builder.WriteString(applyStyle.Render(applyLabel))
	builder.WriteString("  ")
	builder.WriteString(rejectStyle.Render(rejectLabel))
	builder.WriteString("\n\n")

	// Hint separator is `·` (middle dot) with double-space padding so it
	// matches the rest of the app's hint surfaces (welcome panel tips,
	// shortcuts overlay footer, prompt option lines). Previously these
	// two lines used `|` with single spaces, which was visually heavier
	// and inconsistent with everything else.
	var inspection []string
	if m.canScrollDiff() {
		inspection = append(inspection, "j/k Scroll", "g/G Top/Bottom", "Ctrl+D/U Half page")
	}
	if m.canJumpChanges() {
		inspection = append(inspection, "[ ] Prev/Next")
	}
	inspection = append(inspection, "+/- Context", "I Ignore whitespace")
	builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(inspection, "  ·  ")), m.renderWidth(), "…"))
	builder.WriteString("\n")
	builder.WriteString(ansi.Truncate(hintStyle.Render("A Accept all  ·  R Reject all  ·  s Toggle split/unified"), m.renderWidth(), "…"))
}

func (m DiffPreviewModel) renderSideBySide() string {
	leftHeader := lipgloss.NewStyle().Foreground(ColorDim).Bold(true).Render("OLD")
	rightHeader := lipgloss.NewStyle().Foreground(ColorDim).Bold(true).Render("NEW")

	oldLines := strings.Split(m.displayOldContent, "\n")
	newLines := strings.Split(m.displayNewContent, "\n")
	maxLines := len(oldLines)
	if len(newLines) > maxLines {
		maxLines = len(newLines)
	}
	if maxLines == 0 {
		return ""
	}

	leftW := m.viewport.Width/2 - 4
	if leftW < 20 {
		leftW = 20
	}
	rightW := m.viewport.Width - leftW - 7
	if rightW < 20 {
		rightW = 20
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-4s %-*s | %-4s %-*s | Δ\n", "#", leftW, leftHeader, "#", rightW, rightHeader)
	b.WriteString(strings.Repeat("-", leftW+rightW+18))
	b.WriteString("\n")

	addedStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	removedStyle := lipgloss.NewStyle().Foreground(ColorError)
	sameStyle := lipgloss.NewStyle().Foreground(ColorDim)

	for i := 0; i < maxLines; i++ {
		var oldLine, newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}

		left := truncateForWidth(oldLine, leftW)
		right := truncateForWidth(newLine, rightW)

		marker := sameStyle.Render(" ")
		if oldLine != newLine {
			if oldLine == "" && newLine != "" {
				marker = addedStyle.Render("+")
			} else if oldLine != "" && newLine == "" {
				marker = removedStyle.Render("-")
			} else {
				marker = lipgloss.NewStyle().Foreground(ColorWarning).Render("≠")
			}
		}

		fmt.Fprintf(&b, "%-4d %-*s | %-4d %-*s | %s\n",
			i+1, leftW, left, i+1, rightW, right, marker)
	}

	return b.String()
}

func truncateForWidth(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	// x/ansi walks extended grapheme clusters and preserves any SGR reset
	// sequences after the visible cut. Iterating over runes here used to split
	// combining text, emoji modifiers and ZWJ emoji, and could cut directly
	// through a styled escape sequence.
	return ansi.Truncate(s, max, "…")
}

func (m *DiffPreviewModel) detectChangeOffsets(content string) []int {
	lines := strings.Split(content, "\n")
	offsets := make([]int, 0, 16)
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			offsets = append(offsets, i)
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			offsets = append(offsets, i)
		case strings.Contains(line, "| +"), strings.Contains(line, "| -"), strings.Contains(line, "| ≠"):
			offsets = append(offsets, i)
		}
	}
	return offsets
}

func (m *DiffPreviewModel) jumpToChange(next bool) {
	if len(m.changeOffsets) == 0 {
		return
	}

	if next {
		m.currentChange++
		if m.currentChange >= len(m.changeOffsets) {
			m.currentChange = 0
		}
	} else {
		if m.currentChange <= 0 {
			m.currentChange = len(m.changeOffsets) - 1
		} else {
			m.currentChange--
		}
	}

	target := m.changeOffsets[m.currentChange]
	m.viewport.SetYOffset(target)
}

// GetDecision returns the current decision.
func (m DiffPreviewModel) GetDecision() DiffDecision {
	return m.decision
}

// GetFilePath returns the file path being previewed.
func (m DiffPreviewModel) GetFilePath() string {
	return m.filePath
}

// GetNewContent returns the new content for the file.
func (m DiffPreviewModel) GetNewContent() string {
	return m.newContent
}

// DiffFile represents a file in a multi-file diff.
type DiffFile struct {
	FilePath   string
	OldContent string
	NewContent string
	IsNewFile  bool
	Diff       string
}

// MultiDiffPreviewModel is the UI component for displaying multi-file diff previews.
type MultiDiffPreviewModel struct {
	files               []DiffFile
	currentIndex        int
	listOffset          int
	viewport            viewport.Model
	decisions           map[int]DiffDecision
	styles              *Styles
	width               int
	height              int
	focusOnList         bool // true if focus is on file list, false if on diff
	confirmFinish       bool
	responseUnavailable bool
	responsePending     bool // completion command is already on its way to the parent
	requestID           string
	terminalSizeKnown   bool

	// Callback when user makes decisions
	onComplete func(decisions map[string]DiffDecision)
}

// MultiDiffPreviewRequestMsg is sent to request a multi-file diff preview.
type MultiDiffPreviewRequestMsg struct {
	RequestID string
	Files     []DiffFile
}

// MultiDiffPreviewResponseMsg is sent when user completes multi-file decisions.
type MultiDiffPreviewResponseMsg struct {
	RequestID  string
	Decisions  map[string]DiffDecision
	triggerKey string
}

// NewMultiDiffPreviewModel creates a new multi-file diff preview model.
func NewMultiDiffPreviewModel(styles *Styles) MultiDiffPreviewModel {
	vp := viewport.New(80, 20)
	vp.MouseWheelEnabled = true

	return MultiDiffPreviewModel{
		viewport:    vp,
		styles:      styles,
		decisions:   make(map[int]DiffDecision),
		focusOnList: true,
	}
}

// SetSize sets the size of the multi-diff preview.
func (m *MultiDiffPreviewModel) SetSize(width, height int) {
	m.terminalSizeKnown = width > 0 && height > 0
	m.width = max(width, 1)
	m.height = max(height, 1)
	if m.useMultiDiffSplitLayout() {
		listWidth := max(m.width/3, 22)
		m.viewport.Width = max(m.width-listWidth-5, 1)
	} else {
		m.viewport.Width = max(m.width-4, 1)
	}
	reservedRows := 13
	if m.height < 16 {
		reservedRows = 11
	} else if m.width >= 40 && !m.useMultiDiffSplitLayout() {
		reservedRows = 15
	}
	m.viewport.Height = max(m.height-reservedRows, 1)
	m.ensureCurrentFileVisible()
}

const minMultiDiffSplitWidth = 72

func (m MultiDiffPreviewModel) useMultiDiffSplitLayout() bool {
	return m.width >= minMultiDiffSplitWidth
}

func (m MultiDiffPreviewModel) compactMultiDiffLayout() bool {
	return m.width < 40 || (m.height > 0 && m.height < 16)
}

func (m MultiDiffPreviewModel) renderWidth() int {
	if m.width > 0 {
		return m.width
	}
	return max(m.viewport.Width+4, 1)
}

func (m MultiDiffPreviewModel) canNavigateFiles() bool {
	return len(m.files) > 1
}

func (m MultiDiffPreviewModel) canScrollDiff() bool {
	return len(m.files) > 0 && m.viewport.TotalLineCount() > m.viewport.Height
}

func (m MultiDiffPreviewModel) inspectionHints(compact bool) []string {
	var hints []string
	if m.focusOnList {
		if m.canNavigateFiles() {
			label := "↑/↓ or j/k Navigate files"
			if compact {
				label = "↑/↓ Files"
			}
			hints = append(hints, label)
		}
		if m.canScrollDiff() {
			label := "Tab Focus diff"
			if compact {
				label = "Tab Diff"
			}
			hints = append(hints, label)
		}
	} else {
		if m.canScrollDiff() {
			label := "↑/↓ or j/k Scroll diff"
			if compact {
				label = "j/k Scroll"
			}
			hints = append(hints, label)
		}
		if m.canNavigateFiles() {
			label := "Tab Focus files"
			if compact {
				label = "Tab Files"
			}
			hints = append(hints, label)
		}
	}
	return hints
}

// SetFiles sets the files to display.
func (m *MultiDiffPreviewModel) SetFiles(files []DiffFile) {
	m.files = append([]DiffFile(nil), files...)
	m.currentIndex = -1
	m.listOffset = 0
	m.focusOnList = true
	m.confirmFinish = false
	m.responseUnavailable = false
	m.responsePending = false
	m.decisions = make(map[int]DiffDecision)

	// Initialize all decisions to pending
	for i := range m.files {
		m.decisions[i] = DiffPending
	}

	// Generate diffs for all files
	for i := range m.files {
		m.files[i].Diff = m.generateDiff(i)
	}

	// Show first file's diff
	if len(m.files) > 0 {
		m.currentIndex = 0
		m.updateViewport()
	} else {
		m.viewport.SetContent("")
		m.viewport.GotoTop()
	}
}

func (m *MultiDiffPreviewModel) SetRequestID(requestID string) {
	m.requestID = requestID
}

func (m MultiDiffPreviewModel) visibleFileCapacity() int {
	// The list header and its spacer consume two content rows.
	return max(m.viewport.Height-2, 1)
}

func (m *MultiDiffPreviewModel) ensureCurrentFileVisible() {
	if len(m.files) == 0 || m.currentIndex < 0 {
		m.listOffset = 0
		return
	}
	capacity := m.visibleFileCapacity()
	if m.currentIndex < m.listOffset {
		m.listOffset = m.currentIndex
	} else if m.currentIndex >= m.listOffset+capacity {
		m.listOffset = m.currentIndex - capacity + 1
	}
	maxOffset := max(len(m.files)-capacity, 0)
	m.listOffset = min(max(m.listOffset, 0), maxOffset)
}

// SetCompleteCallback sets the callback for when user completes decisions.
func (m *MultiDiffPreviewModel) SetCompleteCallback(callback func(map[string]DiffDecision)) {
	m.onComplete = callback
}

func (m *MultiDiffPreviewModel) MarkResponseUnavailable() {
	m.responseUnavailable = true
	m.responsePending = false
	m.confirmFinish = false
}

// generateDiff creates a diff for the file at the given index.
func (m *MultiDiffPreviewModel) generateDiff(index int) string {
	if index < 0 || index >= len(m.files) {
		return ""
	}

	file := m.files[index]
	displayPath := safeKeyEntryText(file.FilePath)
	displayOld := visibleTerminalControlText(file.OldContent)
	displayNew := visibleTerminalControlText(file.NewContent)
	dmp := diffmatchpatch.New()

	var result strings.Builder
	fmt.Fprintf(&result, "--- %s\n", displayPath)
	fmt.Fprintf(&result, "+++ %s\n", displayPath)

	diffs := dmp.DiffMain(displayOld, displayNew, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	for _, d := range diffs {
		lines := strings.Split(d.Text, "\n")
		for i, line := range lines {
			if i == len(lines)-1 && line == "" {
				continue
			}
			switch d.Type {
			case diffmatchpatch.DiffEqual:
				fmt.Fprintf(&result, " %s\n", line)
			case diffmatchpatch.DiffDelete:
				fmt.Fprintf(&result, "-%s\n", line)
			case diffmatchpatch.DiffInsert:
				fmt.Fprintf(&result, "+%s\n", line)
			}
		}
	}

	return result.String()
}

// highlightMultiDiff applies syntax highlighting to the diff. Uses the
// current file's path for language detection so context lines get
// proper chroma syntax coloring (matching single-file DiffPreviewModel
// behaviour from the main diff path).
func (m *MultiDiffPreviewModel) highlightMultiDiff(diff string) string {
	addedStyle := lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
	removedStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	hunkStyle := lipgloss.NewStyle().Foreground(ColorPrimary)
	headerStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
	contextStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	lang := ""
	if m.currentIndex >= 0 && m.currentIndex < len(m.files) {
		lang = diffSyntaxHighlighter.DetectLanguage(m.files[m.currentIndex].FilePath)
	}

	lines := strings.Split(diff, "\n")
	var result strings.Builder

	for _, line := range lines {
		var styledLine string
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			styledLine = headerStyle.Render(line)
		case strings.HasPrefix(line, "@@"):
			styledLine = hunkStyle.Render(line)
		case strings.HasPrefix(line, "+"):
			styledLine = addedStyle.Render(line)
		case strings.HasPrefix(line, "-"):
			styledLine = removedStyle.Render(line)
		default:
			styledLine = renderContextLineSyntax(line, lang, contextStyle)
		}
		result.WriteString(styledLine)
		result.WriteString("\n")
	}

	return result.String()
}

// updateViewport updates the viewport with the current file's diff.
func (m *MultiDiffPreviewModel) updateViewport() {
	if m.currentIndex >= 0 && m.currentIndex < len(m.files) {
		diff := m.files[m.currentIndex].Diff
		m.viewport.SetContent(m.highlightMultiDiff(diff))
		m.viewport.GotoTop()
		m.ensureCurrentFileVisible()
		return
	}
	m.viewport.SetContent("")
	m.viewport.GotoTop()
}

// Init initializes the multi-diff preview model.
func (m MultiDiffPreviewModel) Init() tea.Cmd {
	return nil
}

// Update handles input events for the multi-file diff preview.
func (m MultiDiffPreviewModel) Update(msg tea.Msg) (MultiDiffPreviewModel, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.responsePending {
			switch msg.String() {
			case "y", "Y", "n", "N", "A", "R", "enter", "esc":
				// The completion command owns an immutable snapshot. Navigation and
				// viewport inspection may continue while the parent consumes it, but
				// no key may mutate decisions or arm another completion state.
				return m, nil
			}
		}
		if m.responseUnavailable {
			switch msg.String() {
			case "esc":
				m.setAllDecisions(DiffReject)
				return m, m.finish(msg.String())
			case "y", "Y", "n", "N", "A", "R", "enter":
				return m, nil
			}
		}
		if m.confirmFinish {
			switch msg.String() {
			case "enter":
				if m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
					return m, nil
				}
				m.confirmFinish = false
				m.applyPending(DiffReject)
				return m, m.finish(msg.String())
			case "esc", "q":
				m.confirmFinish = false
			}
			return m, nil
		}

		switch msg.String() {
		case "tab":
			// Toggle focus between file list and diff
			if len(m.files) > 0 {
				m.focusOnList = !m.focusOnList
			}
			return m, nil

		case "up", "k":
			if m.focusOnList {
				// Move up in file list
				if m.currentIndex > 0 {
					m.currentIndex--
					m.updateViewport()
					m.ensureCurrentFileVisible()
				}
			} else {
				// Scroll diff up
				m.viewport, cmd = m.viewport.Update(tea.KeyMsg{Type: tea.KeyUp})
			}
			return m, cmd

		case "down", "j":
			if m.focusOnList {
				// Move down in file list
				if m.currentIndex < len(m.files)-1 {
					m.currentIndex++
					m.updateViewport()
					m.ensureCurrentFileVisible()
				}
			} else {
				// Scroll diff down
				m.viewport, cmd = m.viewport.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
			return m, cmd

		// Per-file decisions (Sprint UI polish): y/n act on the CURRENT
		// file and advance; A/R (shift) still bulk-apply/reject all
		// remaining. Previously every key was an all-or-nothing shortcut
		// which forced mixed-outcome workflows into multiple review rounds.
		case "y", "Y":
			if len(m.files) == 0 || m.currentIndex < 0 || m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
				return m, nil
			}
			m.decisions[m.currentIndex] = DiffApply
			if m.allResolved() {
				return m, m.finish(msg.String())
			}
			m.moveToNextPending()
			return m, nil

		case "n", "N":
			if len(m.files) == 0 || m.currentIndex < 0 {
				return m, nil
			}
			m.decisions[m.currentIndex] = DiffReject
			if m.allResolved() {
				return m, m.finish(msg.String())
			}
			m.moveToNextPending()
			return m, nil

		case "A":
			if len(m.files) == 0 || m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
				return m, nil
			}
			// Bulk apply: every file that's still pending becomes Apply.
			// Already-decided files keep their explicit decision.
			m.applyPending(DiffApply)
			return m, m.finish(msg.String())

		case "R":
			if len(m.files) == 0 {
				return m, nil
			}
			m.applyPending(DiffReject)
			return m, m.finish(msg.String())

		case "enter":
			if len(m.files) == 0 || m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
				return m, nil
			}
			// Finishing early rejects pending files. Make that consequence an
			// explicit second step instead of hiding it behind a generic label.
			m.confirmFinish = true
			return m, nil

		case "esc":
			m.setAllDecisions(DiffReject)
			return m, m.finish(msg.String())

		case "g":
			if !m.focusOnList {
				m.viewport.GotoTop()
			}
			return m, nil

		case "G":
			if !m.focusOnList {
				m.viewport.GotoBottom()
			}
			return m, nil

		case "ctrl+d":
			if !m.focusOnList {
				m.viewport.HalfPageDown()
			}
			return m, nil

		case "ctrl+u":
			if !m.focusOnList {
				m.viewport.HalfPageUp()
			}
			return m, nil
		}

	case tea.MouseMsg:
		if m.focusOnList {
			if direction := verticalMouseWheelDirection(msg); direction != 0 && len(m.files) > 0 {
				step := max(m.viewport.MouseWheelDelta, 1)
				m.currentIndex = min(max(m.currentIndex+direction*step, 0), len(m.files)-1)
				m.updateViewport()
				m.ensureCurrentFileVisible()
			}
		} else {
			m.viewport, cmd = m.viewport.Update(msg)
		}
		return m, cmd

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		return m, nil
	}

	return m, nil
}

func (m *MultiDiffPreviewModel) setAllDecisions(decision DiffDecision) {
	for i := range m.files {
		m.decisions[i] = decision
	}
}

// applyPending sets `decision` on every file that's still DiffPending —
// files already explicitly accepted/rejected keep their value. Used by the
// bulk "A"/"R"/Enter commands after the user has been through per-file
// review.
func (m *MultiDiffPreviewModel) applyPending(decision DiffDecision) {
	for i := range m.files {
		if m.decisions[i] == DiffPending {
			m.decisions[i] = decision
		}
	}
}

// allResolved reports whether every file has an explicit Apply/Reject.
// When true the UI auto-finishes instead of sitting on an empty state.
func (m *MultiDiffPreviewModel) allResolved() bool {
	for i := range m.files {
		if m.decisions[i] == DiffPending {
			return false
		}
	}
	return true
}

// moveToNextPending moves to the next file with pending decision.
func (m *MultiDiffPreviewModel) moveToNextPending() {
	// First, try to find next pending after current
	for i := m.currentIndex + 1; i < len(m.files); i++ {
		if m.decisions[i] == DiffPending {
			m.currentIndex = i
			m.updateViewport()
			return
		}
	}
	// Then try from beginning
	for i := 0; i < m.currentIndex; i++ {
		if m.decisions[i] == DiffPending {
			m.currentIndex = i
			m.updateViewport()
			return
		}
	}
	// Stay on current if no pending found
}

// finish creates the completion message.
func (m *MultiDiffPreviewModel) finish(triggerKey string) tea.Cmd {
	if m.responsePending {
		return nil
	}
	m.responsePending = true
	decisions := make(map[string]DiffDecision)
	for i, file := range m.files {
		decisions[file.FilePath] = m.decisions[i]
	}

	if m.onComplete != nil {
		m.onComplete(cloneDiffDecisions(decisions))
	}

	return func() tea.Msg {
		return MultiDiffPreviewResponseMsg{
			RequestID:  m.requestID,
			Decisions:  cloneDiffDecisions(decisions),
			triggerKey: triggerKey,
		}
	}
}

func cloneDiffDecisions(decisions map[string]DiffDecision) map[string]DiffDecision {
	result := make(map[string]DiffDecision, len(decisions))
	for path, decision := range decisions {
		result[path] = decision
	}
	return result
}

// View renders the multi-file diff preview.
func (m MultiDiffPreviewModel) View() string {
	if m.height > 0 && m.height <= 8 {
		return m.renderTinyView()
	}

	var builder strings.Builder

	// Header
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorHighlight).
		Padding(0, 1)

	title := fmt.Sprintf("Multi-File Diff Preview (%d files)", len(m.files))
	compact := m.compactMultiDiffLayout()
	if compact {
		title = fmt.Sprintf("Multi-File Diff · %d files", len(m.files))
	}
	builder.WriteString(ansi.Truncate(headerStyle.Render(title), m.renderWidth(), "…"))
	if compact {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n")
	}
	if len(m.files) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		builder.WriteString(emptyStyle.Render(truncateForWidth("No files to review", m.renderWidth())))
		builder.WriteString("\n")
		builder.WriteString(emptyStyle.Render(truncateForWidth("The requested diff contains no file changes.", m.renderWidth())))
		builder.WriteString("\n\n")
		m.renderMultiActions(&builder)
		return builder.String()
	}

	// Calculate widths
	listWidth := max(m.width/3, 22)

	// Render file list
	listStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Width(listWidth - 2).
		Height(m.viewport.Height)

	if m.focusOnList {
		listStyle = listStyle.BorderForeground(ColorHighlight)
	}

	var listContent strings.Builder
	capacity := m.visibleFileCapacity()
	start := min(max(m.listOffset, 0), max(len(m.files)-capacity, 0))
	end := min(start+capacity, len(m.files))
	listContent.WriteString(lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("Files %d–%d/%d", start+1, end, len(m.files))))
	listContent.WriteString("\n\n")

	for i := start; i < end; i++ {
		file := m.files[i]
		fileName := filepath.Base(safeKeyEntryText(file.FilePath))

		// Decision marker — lets users see which files they've already
		// reviewed without switching off the current file.
		var statusMark string
		switch m.decisions[i] {
		case DiffApply:
			statusMark = lipgloss.NewStyle().Foreground(ColorSuccess).Render("✓")
		case DiffReject:
			statusMark = lipgloss.NewStyle().Foreground(ColorError).Render("✗")
		default:
			statusMark = lipgloss.NewStyle().Foreground(ColorDim).Render("·")
		}

		lineStyle := lipgloss.NewStyle()
		marker := " "
		if i == m.currentIndex {
			lineStyle = lineStyle.Background(ColorBorder).Bold(true)
			marker = ">"
		}

		linePrefix := fmt.Sprintf(" %s %s ", marker, statusMark)
		nameBudget := max(listWidth-2-lipgloss.Width(linePrefix)-1, 1)
		fileName = truncateMiddleForWidth(fileName, nameBudget)
		line := linePrefix + fileName
		listContent.WriteString(lineStyle.Render(ansi.Truncate(line, max(listWidth-2, 1), "…")))
		listContent.WriteString("\n")
	}

	// Render diff viewport
	viewportStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	if !m.focusOnList {
		viewportStyle = viewportStyle.BorderForeground(ColorHighlight)
	}

	// Current file info
	var fileInfo string
	if m.currentIndex >= 0 && m.currentIndex < len(m.files) {
		file := m.files[m.currentIndex]
		label := "Modified: "
		if file.IsNewFile {
			label = "New file: "
		}
		if compact {
			kind := "~"
			if file.IsNewFile {
				kind = "+"
			}
			label = fmt.Sprintf("%d/%d %s ", m.currentIndex+1, len(m.files), kind)
		} else if !m.useMultiDiffSplitLayout() {
			label = fmt.Sprintf("File %d/%d · %s", m.currentIndex+1, len(m.files), label)
		}
		displayPath := safeKeyEntryText(file.FilePath)
		pathBudget := max(m.width-lipgloss.Width(label)-1, 1)
		display := label + truncateLeftForWidth(displayPath, pathBudget)
		fileInfo = lipgloss.NewStyle().
			Foreground(ColorAccent).
			Bold(true).
			Render(display)
	}

	// Layout: file list on left, diff on right
	rightPane := viewportStyle.Render(m.viewport.View())

	builder.WriteString(fileInfo)
	builder.WriteString("\n\n")
	if m.useMultiDiffSplitLayout() {
		leftPane := listStyle.Render(strings.TrimSuffix(listContent.String(), "\n"))
		builder.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane))
	} else {
		builder.WriteString(rightPane)
	}
	builder.WriteString("\n\n")

	// Footer with actions
	m.renderMultiActions(&builder)

	return builder.String()
}

func (m MultiDiffPreviewModel) renderTinyView() string {
	width := max(m.renderWidth(), 1)
	identityStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	statusStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
	rowBudget := max(m.height-2, 1)

	if len(m.files) == 0 {
		identity := ansi.Truncate(identityStyle.Render("Multi-diff · no files"), width, "…")
		closeHint := ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc")+" Close"), width, "…")
		return renderTinyReviewRows(identity, "", []string{closeHint}, rowBudget)
	}

	index := min(max(m.currentIndex, 0), len(m.files)-1)
	filePath := safeKeyEntryText(m.files[index].FilePath)
	if filePath == "" {
		filePath = "(unknown file)"
	}
	prefix := fmt.Sprintf("%d/%d · ", index+1, len(m.files))
	identity := prefix + truncateLeftForWidth(filePath, max(width-lipgloss.Width(prefix), 1))
	identity = ansi.Truncate(identityStyle.Render(identity), width, "…")

	applied, rejected, pending := 0, 0, 0
	for i := range m.files {
		switch m.decisions[i] {
		case DiffApply:
			applied++
		case DiffReject:
			rejected++
		default:
			pending++
		}
	}
	status := fmt.Sprintf("%d pending · %d✓ %d✗", pending, applied, rejected)
	if len(m.files) > 1 {
		status = "↑↓ Files · " + status
	}
	status = ansi.Truncate(statusStyle.Render(status), width, "…")

	if m.responseUnavailable {
		warning := ansi.Truncate(warningStyle.Render("Decisions unavailable"), width, "…")
		cancel := ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc")+" Cancel"), width, "…")
		return renderTinyReviewRows(identity, warning, []string{cancel}, rowBudget)
	}
	if m.responsePending {
		pendingLine := ansi.Truncate(statusStyle.Render("Submitting decisions…"), width, "…")
		return renderTinyReviewRows(identity, status, []string{pendingLine}, rowBudget)
	}
	if m.terminalSizeKnown && tinyReviewNeedsResize(m.width, m.height) {
		resize := warningStyle.Render(tinyReviewResizeLabel(width))
		safeAction := keyStyle.Render("n")
		if m.confirmFinish {
			switch {
			case width >= 5:
				safeAction = keyStyle.Render("Esc/q")
			case width >= 3:
				safeAction = keyStyle.Render("Esc")
			default:
				safeAction = keyStyle.Render("q")
			}
		} else if width >= 4 {
			safeAction += hintStyle.Render(" No")
		}
		return renderTinyResizeRows(identity, resize, safeAction, rowBudget)
	}
	if m.confirmFinish {
		warning := fmt.Sprintf("Reject %d pending and finish?", pending)
		controls := keyStyle.Render("Enter") + hintStyle.Render(" Confirm · ") + keyStyle.Render("Esc")
		controls = ansi.Truncate(controls, width, "…")
		if rowBudget <= 2 {
			suffix := " · Reject"
			subject := warningStyle.Render("Reject")
			if width > lipgloss.Width(suffix) {
				subject = identityStyle.Render(truncateLeftForWidth(filePath, width-lipgloss.Width(suffix)) + suffix)
			}
			return strings.Join([]string{ansi.Truncate(subject, width, "…"), controls}, "\n")
		}
		return renderTinyReviewRows(
			identity,
			ansi.Truncate(warningStyle.Render(warning), width, "…"),
			[]string{controls},
			rowBudget,
		)
	}

	primary := keyStyle.Render("y") + " Apply · " + keyStyle.Render("n") + " Reject"
	recovery := keyStyle.Render("Esc") + " Reject all"
	return renderTinyReviewRows(
		identity,
		status,
		[]string{
			ansi.Truncate(hintStyle.Render(primary), width, "…"),
			ansi.Truncate(hintStyle.Render(recovery), width, "…"),
		},
		rowBudget,
	)
}

// renderMultiActions renders the action buttons for multi-file diff.
func (m MultiDiffPreviewModel) renderMultiActions(builder *strings.Builder) {
	applyStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText).
		Background(ColorSuccess).
		Padding(0, 1)

	rejectStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText).
		Background(ColorError).
		Padding(0, 1)

	allStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorText).
		Background(ColorBorder).
		Padding(0, 1)

	hintStyle := lipgloss.NewStyle().
		Foreground(ColorDim)
	if len(m.files) == 0 {
		keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		builder.WriteString(ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc")+" Close"), m.renderWidth(), "…"))
		return
	}

	// Count resolved vs pending for the status line.
	applied, rejected, pending := 0, 0, 0
	for i := range m.files {
		switch m.decisions[i] {
		case DiffApply:
			applied++
		case DiffReject:
			rejected++
		default:
			pending++
		}
	}

	statusStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	var status string
	if m.width < 60 || m.compactMultiDiffLayout() {
		status = fmt.Sprintf("%d/%d · %d pending · %d✓ %d✗", m.currentIndex+1, len(m.files), pending, applied, rejected)
	} else {
		status = fmt.Sprintf(
			"File %d/%d | %d✓ apply · %d✗ reject · %d· pending",
			m.currentIndex+1,
			len(m.files),
			applied,
			rejected,
			pending,
		)
	}
	builder.WriteString(ansi.Truncate(statusStyle.Render(status), m.renderWidth(), "…"))
	if m.responseUnavailable {
		warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		builder.WriteString("\n")
		builder.WriteString(ansi.Truncate(warningStyle.Render("Unavailable: cannot submit diff decisions"), m.renderWidth(), "…"))
		builder.WriteString("\n")
		keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		builder.WriteString(ansi.Truncate(hintStyle.Render(keyStyle.Render("Esc")+" Cancel · view controls remain available"), m.renderWidth(), "…"))
		if inspection := m.inspectionHints(m.compactMultiDiffLayout()); len(inspection) > 0 {
			builder.WriteString("\n")
			builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(inspection, "  ·  ")), m.renderWidth(), "…"))
		}
		return
	}
	if m.confirmFinish {
		warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		fileLabel := "files"
		if pending == 1 {
			fileLabel = "file"
		}
		warning := fmt.Sprintf("Reject %d pending %s and finish?", pending, fileLabel)
		builder.WriteString("\n")
		builder.WriteString(ansi.Truncate(warningStyle.Render(warning), m.renderWidth(), "…"))
		builder.WriteString("\n")
		keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		// Back is the non-destructive recovery path. Keep it before confirmation
		// so a narrow footer never shows only the action that rejects pending files.
		controls := keyStyle.Render("Esc/q") + " Back to review  ·  " + keyStyle.Render("Enter") + " Confirm"
		builder.WriteString(ansi.Truncate(hintStyle.Render(controls), m.renderWidth(), "…"))
		return
	}
	if m.compactMultiDiffLayout() {
		keyStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)
		writeHint := func(line string) {
			builder.WriteString("\n")
			builder.WriteString(ansi.Truncate(hintStyle.Render(line), m.renderWidth(), "…"))
		}
		if m.renderWidth() < 20 {
			writeHint(keyStyle.Render("y") + " Yes " + keyStyle.Render("n") + " No")
			writeHint(keyStyle.Render("A") + " All " + keyStyle.Render("R") + " No")
			writeHint("Esc Reject")
			writeHint("Enter Done…")
			return
		}
		writeHint(keyStyle.Render("y") + " Apply  ·  " + keyStyle.Render("n") + " Reject")
		writeHint(keyStyle.Render("A") + " Apply rest  ·  " + keyStyle.Render("R") + " Reject rest")
		if inspection := m.inspectionHints(true); len(inspection) > 0 {
			writeHint(strings.Join(inspection, "  ·  "))
		}
		writeHint("Esc Reject all  ·  Enter Finish…")
		return
	}
	builder.WriteString("\n\n")

	builder.WriteString(applyStyle.Render("y Apply file"))
	if m.width < 40 {
		builder.WriteString("\n")
	} else {
		builder.WriteString("  ")
	}
	builder.WriteString(rejectStyle.Render("n Reject file"))
	if m.width < 80 || !m.useMultiDiffSplitLayout() {
		builder.WriteString("\n")
	} else {
		builder.WriteString("  ")
	}
	builder.WriteString(allStyle.Render("A Apply remaining"))
	if m.width < 40 {
		builder.WriteString("\n")
	} else {
		builder.WriteString("  ")
	}
	builder.WriteString(allStyle.Render("R Reject remaining"))
	builder.WriteString("\n\n")

	if m.width >= 40 {
		inspection := m.inspectionHints(false)
		if m.useMultiDiffSplitLayout() {
			actions := []string{"Esc Reject all", "Enter Finish…", "y/n Per-file", "A/R Bulk"}
			actions = append(actions, inspection...)
			builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(actions, "  ·  ")), m.renderWidth(), "…"))
		} else {
			inspection = append(inspection, "Enter Finish…")
			builder.WriteString(ansi.Truncate(hintStyle.Render(strings.Join(inspection, "  ·  ")), m.renderWidth(), "…"))
			builder.WriteString("\n")
			builder.WriteString(ansi.Truncate(hintStyle.Render("Esc Reject all  ·  y/n Per-file  ·  A/R Bulk"), m.renderWidth(), "…"))
		}
	}
}

// GetDecisions returns the current decisions map.
func (m MultiDiffPreviewModel) GetDecisions() map[string]DiffDecision {
	decisions := make(map[string]DiffDecision)
	for i, file := range m.files {
		decisions[file.FilePath] = m.decisions[i]
	}
	return decisions
}
