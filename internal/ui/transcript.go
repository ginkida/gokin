package ui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

type transcriptView struct {
	conversation, tools  string
	toolTab              bool
	lines                []string
	logicalLines         []string
	logicalRows          []int
	width, offset        int
	query, previousQuery string
	searching            bool
	matches              []int
	matchRanges          []transcriptMatchRange
	matchIndex           int
}

type transcriptMatchRange struct {
	startRow, endRow, startByte, endByte int
}

type transcriptToolResult struct {
	name, context, content string
	sequence               int
}

func (m *Model) recordTranscriptTool(msg ToolResultMsg, context string) {
	if msg.Content == "" && msg.Diff == "" && msg.Error == "" {
		return
	}
	name := msg.Name
	content := msg.Content
	if msg.Failed {
		name += " · failed"
		if msg.Error != "" {
			content = msg.Error + "\n\n" + content
		}
	}
	if msg.Diff != "" {
		content += "\n\nChanges:\n" + msg.Diff
	}
	m.transcriptToolSeq++
	m.transcriptTools = append(m.transcriptTools, transcriptToolResult{name, context, content, m.transcriptToolSeq})
	if len(m.transcriptTools) > 1000 {
		m.transcriptTools = append([]transcriptToolResult(nil), m.transcriptTools[len(m.transcriptTools)-1000:]...)
		m.transcriptToolsDropped = true
	}
}

func transcriptText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

func (m *Model) refreshTranscript() {
	t := m.transcript
	t.conversation = transcriptText(m.output.Content())
	var tools strings.Builder
	if len(m.transcriptTools) > 0 {
		if m.transcriptToolsDropped {
			tools.WriteString("Most recent 1000 tool results; earlier output remains in Conversation.\n\n")
		}
		for _, entry := range m.transcriptTools {
			fmt.Fprintf(&tools, "── %s #%d · %s ──\n%s\n\n", transcriptText(entry.name), entry.sequence, transcriptText(entry.context), transcriptText(entry.content))
		}
	} else if m.toolOutput != nil {
		for _, entry := range m.toolOutput.entries {
			fmt.Fprintf(&tools, "── %s #%d ──\n%s\n\n", transcriptText(entry.ToolName), entry.Index+1, transcriptText(entry.FullContent))
		}
	}
	t.tools = tools.String()
	t.width = 0
	t.reflow(m.width)
}

func (m *Model) openTranscript() {
	if m.transcript == nil {
		m.transcript = &transcriptView{matchIndex: -1}
	}
	m.refreshTranscript()
	m.transcript.offset = max(len(m.transcript.lines)-max(m.height-3, 1), 0)
	m.enterTransientOverlay(StateTranscript)
}

func (t *transcriptView) reflow(width int) {
	width = max(width-2, 1)
	if width == t.width {
		return
	}
	// Preserve the source position the reader is looking at when a terminal
	// resize changes wrapping. A visual row number alone points elsewhere
	// once the same history is laid out in wider or narrower columns.
	anchorLine, anchorByte := -1, 0
	if t.width > 0 && t.offset >= 0 && t.offset < len(t.lines) && len(t.logicalRows) > 0 {
		anchorLine = sort.Search(len(t.logicalRows), func(i int) bool { return t.logicalRows[i] > t.offset }) - 1
		for row := t.logicalRows[anchorLine]; row < t.offset; row++ {
			anchorByte += len(t.lines[row])
		}
	}
	t.width = width
	content := t.conversation
	if t.toolTab {
		content = t.tools
	}
	if strings.TrimSpace(content) == "" {
		content = "No conversation yet"
		if t.toolTab {
			content = "No tool results yet"
		}
	}
	t.lines = nil
	t.logicalLines = strings.Split(content, "\n")
	t.logicalRows = nil
	for _, line := range t.logicalLines {
		t.logicalRows = append(t.logicalRows, len(t.lines))
		t.lines = append(t.lines, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	if anchorLine >= 0 && anchorLine < len(t.logicalRows) {
		t.offset = t.logicalRows[anchorLine]
		end := len(t.lines)
		if anchorLine+1 < len(t.logicalRows) {
			end = t.logicalRows[anchorLine+1]
		}
		for t.offset+1 < end && anchorByte >= len(t.lines[t.offset]) {
			anchorByte -= len(t.lines[t.offset])
			t.offset++
		}
	}
	t.findMatches()
}

func (t *transcriptView) findMatches() {
	t.matches = nil
	t.matchRanges = nil
	if t.query != "" {
		query := regexp.MustCompile("(?i)" + regexp.QuoteMeta(t.query))
		for i, line := range t.logicalLines {
			row := t.logicalRows[i]
			end := len(t.lines)
			if i+1 < len(t.logicalRows) {
				end = t.logicalRows[i+1]
			}
			rowEndByte := len(t.lines[row])
			for _, match := range query.FindAllStringIndex(line, -1) {
				// Wrapped rows preserve the original bytes. Walk them once for
				// ordered matches, including matches inside a grapheme cluster.
				// Rewrapping every prefix made dense tool output quadratic.
				for row+1 < end && match[0] >= rowEndByte {
					row++
					rowEndByte += len(t.lines[row])
				}
				t.matches = append(t.matches, row)
				endRow, endByte := row, rowEndByte
				for endRow+1 < end && match[1] > endByte {
					endRow++
					endByte += len(t.lines[endRow])
				}
				t.matchRanges = append(t.matchRanges, transcriptMatchRange{
					startRow: row, endRow: endRow,
					startByte: match[0] - (rowEndByte - len(t.lines[row])),
					endByte:   match[1] - (endByte - len(t.lines[endRow])),
				})
			}
		}
	}
	t.matchIndex = -1
	for i, line := range t.matches {
		if line >= t.offset {
			t.matchIndex = i
			break
		}
	}
	if t.matchIndex == -1 && len(t.matches) > 0 {
		t.matchIndex = 0
	}
}

// highlightRow styles only visible rows. Search ranges use source bytes; extend
// their display to whole graphemes so combining marks and ZWJ emoji stay intact.
func (t *transcriptView) highlightRow(row int) string {
	line := t.lines[row]
	if len(t.matchRanges) == 0 {
		return line
	}
	first := sort.SearchInts(t.matches, row)
	if first > 0 && t.matchRanges[first-1].endRow >= row {
		first--
	}
	last := sort.Search(len(t.matches), func(i int) bool { return t.matches[i] > row })
	if first >= last {
		return line
	}
	normal := lipgloss.NewStyle().Foreground(ColorWarning).Underline(true)
	selected := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorWarning).Bold(true)
	var result strings.Builder
	class, runStart, candidate := 0, 0, first
	flush := func(end int) {
		if end <= runStart {
			return
		}
		text := line[runStart:end]
		switch class {
		case 1:
			result.WriteString(normal.Render(text))
		case 2:
			result.WriteString(selected.Render(text))
		default:
			result.WriteString(text)
		}
	}
	graphemes := uniseg.NewGraphemes(line)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		nextClass := 0
		for candidate < last && t.matchRanges[candidate].endRow == row && t.matchRanges[candidate].endByte <= start {
			candidate++
		}
		for hitIndex := candidate; hitIndex < last; hitIndex++ {
			hit := t.matchRanges[hitIndex]
			if hit.endRow == row && hit.endByte <= start {
				continue
			}
			if hit.startRow == row && hit.startByte >= end {
				break
			}
			nextClass = 1
			if hitIndex == t.matchIndex {
				nextClass = 2
				break
			}
		}
		if nextClass != class {
			flush(start)
			runStart, class = start, nextClass
		}
	}
	flush(len(line))
	return result.String()
}

func (t *transcriptView) jumpMatch(delta int) {
	if len(t.matches) == 0 {
		return
	}
	t.matchIndex = (t.matchIndex + delta + len(t.matches)) % len(t.matches)
	t.offset = t.matches[t.matchIndex]
}

func (m *Model) handleTranscriptKeys(msg tea.KeyMsg) tea.Cmd {
	t := m.transcript
	if t == nil {
		return m.restoreTransientOverlay()
	}
	// Terminal reads can coalesce fast typing into one rune event. Process
	// commands in order so "/query" starts search and retains the query.
	if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste && !t.searching {
		for _, r := range msg.Runes {
			cmd := m.handleTranscriptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: msg.Alt})
			if m.state != StateTranscript {
				return cmd
			}
		}
		return keyConsumed
	}
	if msg.Paste && !t.searching {
		return keyConsumed
	}
	t.reflow(m.width)
	if t.searching {
		switch msg.Type {
		case tea.KeyEsc:
			t.query = t.previousQuery
			t.searching = false
			t.findMatches()
		case tea.KeyEnter:
			t.searching = false
		case tea.KeyBackspace:
			t.query = removeLastGrapheme(t.query)
			t.findMatches()
			t.jumpMatch(0)
		case tea.KeyRunes, tea.KeySpace:
			value := string(msg.Runes)
			if msg.Type == tea.KeySpace {
				value = " "
			}
			if len(t.query)+len(value) <= 256 {
				t.query += strings.ReplaceAll(transcriptText(value), "\n", " ")
				t.findMatches()
				t.jumpMatch(0)
			}
		}
		return keyConsumed
	}
	page := max(m.height-4, 1)
	switch msg.String() {
	case "ctrl+c":
		if state := m.activeRequestState(); state == StateProcessing || state == StateStreaming {
			m.state = state
			m.transientOverlayReturnState = StateInput
			return m.interruptActiveRequest()
		}
		return m.restoreTransientOverlay()
	case "esc", "ctrl+o", "q":
		return m.restoreTransientOverlay()
	case "tab":
		t.toolTab = !t.toolTab
		t.width = 0
		t.offset = 0
		t.reflow(m.width)
	case "/":
		t.previousQuery = t.query
		t.query = ""
		t.searching = true
		t.findMatches()
	case "n":
		t.jumpMatch(1)
	case "N":
		t.jumpMatch(-1)
	case "up", "k", "ctrl+p":
		t.offset--
	case "down", "j", "ctrl+n":
		t.offset++
	case "pgup", "ctrl+b":
		t.offset -= page
	case "pgdown", "ctrl+f", " ":
		t.offset += page
	case "home", "g":
		t.offset = 0
	case "end", "G":
		t.offset = max(len(t.lines)-page, 0)
	case "r":
		m.refreshTranscript()
	}
	t.offset = min(max(t.offset, 0), max(len(t.lines)-max(m.height-3, 1), 0))
	return keyConsumed
}

func (m Model) renderTranscript() string {
	if m.transcript == nil {
		return ""
	}
	t := m.transcript
	t.reflow(m.width)
	width, height := max(m.width, 1), max(m.height, 1)
	tab := "Conversation"
	if t.toolTab {
		tab = "Tool results"
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(ColorPrimary).Render(ansi.Truncate("Transcript · "+tab, width, ""))
	if height == 1 {
		return ansi.Truncate("Esc Close · Transcript", width, "")
	}
	bodyHeight := max(height-3, 0)
	start := min(max(t.offset, 0), max(len(t.lines)-bodyHeight, 0))
	end := min(start+bodyHeight, len(t.lines))
	var rows []string
	rows = append(rows, header)
	for i := start; i < end; i++ {
		prefix := "  "
		match := sort.SearchInts(t.matches, i)
		if match < len(t.matches) && t.matches[match] == i {
			prefix = "› "
		}
		rows = append(rows, ansi.Truncate(prefix+t.highlightRow(i), width, ""))
	}
	for len(rows) < height-1 {
		rows = append(rows, "")
	}
	footer := "Esc Close · Tab Switch · / Search · n/N Next · r Refresh"
	if t.searching {
		footer = "Esc Cancel · Enter Keep · /" + t.query
	} else if t.query != "" {
		label := "matches"
		if len(t.matches) == 1 {
			label = "match"
		}
		position := fmt.Sprint(len(t.matches))
		if len(t.matches) > 0 {
			position = fmt.Sprintf("%d/%d", t.matchIndex+1, len(t.matches))
		}
		footer = fmt.Sprintf("Esc Close · %s %s · /%s · n/N Next", position, label, t.query)
	}
	footer += fmt.Sprintf(" · %d–%d/%d", start+1, end, len(t.lines))
	rows = append(rows, lipgloss.NewStyle().Foreground(ColorMuted).Render(ansi.Truncate(footer, width, "")))
	return strings.Join(rows, "\n")
}
