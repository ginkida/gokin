package ui

import (
	"regexp"
	"strings"

	"gokin/internal/highlight"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// RenderedBlockKind distinguishes streaming block types. Code blocks stream
// INCREMENTALLY: a start marker (top border), one block per code line, and an
// end marker (bottom border + full content for the code-block registry).
// Pre-incremental behavior buffered the whole block until the closing fence —
// a long code block meant seconds of frozen output while text streamed
// invisibly into the buffer.
type RenderedBlockKind int

const (
	BlockText RenderedBlockKind = iota
	BlockCodeStart
	BlockCodeLine
	BlockCodeEnd
)

// RenderedBlock represents a rendered piece of content.
type RenderedBlock struct {
	Kind     RenderedBlockKind
	Content  string // text content / one code line / full code (BlockCodeEnd)
	IsCode   bool   // legacy flag: true for BlockCodeEnd (carries the complete block)
	Language string
	Filename string
}

// MarkdownStreamParser parses streaming markdown and detects code blocks.
type MarkdownStreamParser struct {
	buffer       strings.Builder
	highlighter  *highlight.Highlighter
	inCodeBlock  bool
	codeLanguage string
	codeFilename string
	codeContent  strings.Builder
	styles       *Styles
	renderWidth  int // Current transcript-cell budget; zero means unconstrained.

	// Table buffering state: accumulate consecutive table lines before rendering
	inTable   bool
	tableRows []string
}

type markdownTableRow struct {
	cells       []string
	isSeparator bool
}

// codeBlockStartRegex matches code block start: ```lang or ```lang:filename
var codeBlockStartRegex = regexp.MustCompile("^```(\\w*)(?::(.+))?$")

// unorderedListRegex matches unordered list items with leading whitespace: "  - item" or "  * item"
var unorderedListRegex = regexp.MustCompile(`^(\s*)([-*])\s+(.+)$`)

// orderedListRegex matches ordered list items with leading whitespace: "  1. item"
var orderedListRegex = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.+)$`)

// tableRowRegex matches a markdown table row containing pipe separators.
var tableRowRegex = regexp.MustCompile(`^\s*\|.*\|\s*$`)

// tableSeparatorRegex matches a markdown table separator row: | --- | --- |
var tableSeparatorRegex = regexp.MustCompile(`^\s*\|[\s\-:|]+\|\s*$`)

// Markdown inline formatting regexes
var (
	headingRegex        = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)
	blockquoteRegex     = regexp.MustCompile(`^>\s?(.*)$`)
	horizontalRuleRegex = regexp.MustCompile(`^(---+|\*\*\*+|___+)\s*$`)
	boldRegex           = regexp.MustCompile(`\*\*(.+?)\*\*`)
	italicRegex         = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	linkRegex           = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	strikethroughRegex  = regexp.MustCompile(`~~(.+?)~~`)
)

// NewMarkdownStreamParser creates a new streaming markdown parser.
func NewMarkdownStreamParser(styles *Styles) *MarkdownStreamParser {
	return &MarkdownStreamParser{
		highlighter: highlight.New("monokai"),
		styles:      styles,
	}
}

// SetWidth updates the cell budget used by responsive Markdown blocks. It does
// not reset streaming state, so a resize while a table is still arriving can
// use the newest geometry when the table is finally flushed.
func (p *MarkdownStreamParser) SetWidth(width int) {
	p.renderWidth = max(width, 0)
}

// Feed processes a chunk of text and returns any completed blocks.
func (p *MarkdownStreamParser) Feed(chunk string) []RenderedBlock {
	var blocks []RenderedBlock

	// Assistant output is external terminal content. Strip executable terminal
	// controls before buffering it; doing this at the chunk boundary also makes
	// a split escape harmless (a lone ESC cannot survive into the next chunk).
	chunk = safeTerminalDisplayText(chunk)

	// Add chunk to buffer
	p.buffer.WriteString(chunk)
	content := p.buffer.String()

	// Handle empty content
	if content == "" {
		return blocks
	}

	// Process line by line, keeping incomplete lines in buffer
	lines := strings.Split(content, "\n")
	var processedLen int

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		isLastLine := i == len(lines)-1

		// Keep incomplete last line in buffer (no newline at end)
		if isLastLine && !strings.HasSuffix(content, "\n") {
			break
		}

		// Skip empty trailing line from split (artifact of trailing newline)
		if isLastLine && line == "" && strings.HasSuffix(content, "\n") {
			break
		}

		processedLen += len(line) + 1 // +1 for newline

		if p.inCodeBlock {
			// Check for code block end
			if strings.TrimSpace(line) == "```" {
				codeContent := p.codeContent.String()
				if len(codeContent) > 0 && codeContent[len(codeContent)-1] == '\n' {
					codeContent = codeContent[:len(codeContent)-1] // Remove trailing newline
				}

				// End marker: renders as the bottom border; carries the FULL
				// content so the consumer can register it for copy actions.
				blocks = append(blocks, RenderedBlock{
					Kind:     BlockCodeEnd,
					Content:  codeContent,
					IsCode:   true,
					Language: p.codeLanguage,
					Filename: p.codeFilename,
				})

				p.inCodeBlock = false
				p.codeLanguage = ""
				p.codeFilename = ""
				p.codeContent.Reset()
			} else {
				p.codeContent.WriteString(line)
				p.codeContent.WriteString("\n")
				// Stream the line out immediately — highlighted at render
				// time — instead of sitting invisible until the fence closes.
				blocks = append(blocks, RenderedBlock{
					Kind:     BlockCodeLine,
					Content:  line,
					Language: p.codeLanguage,
					Filename: p.codeFilename,
				})
			}
		} else {
			// Check for code block start
			trimmed := strings.TrimSpace(line)
			if matches := codeBlockStartRegex.FindStringSubmatch(trimmed); matches != nil {
				// Flush any buffered table before entering code block
				if p.inTable {
					blocks = append(blocks, p.flushTable()...)
				}
				p.inCodeBlock = true
				p.codeLanguage = matches[1]
				if len(matches) > 2 {
					p.codeFilename = matches[2]
				}
				p.codeContent.Reset()
				blocks = append(blocks, RenderedBlock{
					Kind:     BlockCodeStart,
					Language: p.codeLanguage,
					Filename: p.codeFilename,
				})
			} else if tableRowRegex.MatchString(line) {
				// Accumulate table rows
				p.inTable = true
				p.tableRows = append(p.tableRows, line)
			} else {
				// Non-table line: flush any buffered table first
				if p.inTable {
					blocks = append(blocks, p.flushTable()...)
				}

				// Render full markdown line (headings, blockquotes, hr, lists, inline)
				rendered := p.renderMarkdownLine(line)
				blocks = append(blocks, RenderedBlock{
					Content: rendered + "\n",
					IsCode:  false,
				})
			}
		}
	}

	// Update buffer with remaining content
	if processedLen > 0 {
		p.buffer.Reset()
		if processedLen < len(content) {
			p.buffer.WriteString(content[processedLen:])
		}
	}

	return blocks
}

// Flush returns any remaining content in the buffer.
func (p *MarkdownStreamParser) Flush() []RenderedBlock {
	var blocks []RenderedBlock

	// Give the unterminated transport fragment the same parser path as a line
	// that arrived with '\n'. This is especially important for a final code line
	// (it must stay inside the fence and enter the copy registry) and a final
	// table row (it must participate in column sizing).
	remaining := p.buffer.String()
	p.buffer.Reset()
	if remaining != "" {
		blocks = append(blocks, p.Feed(remaining+"\n")...)
	}

	// Flush any buffered table
	if p.inTable {
		blocks = append(blocks, p.flushTable()...)
	}

	// If we're in a code block, close it: the lines already streamed out
	// incrementally — emit the end marker so the box gets its bottom border
	// and the registry gets the (incomplete but real) content.
	if p.inCodeBlock {
		blocks = append(blocks, RenderedBlock{
			Kind:     BlockCodeEnd,
			Content:  strings.TrimSuffix(p.codeContent.String(), "\n"),
			IsCode:   true,
			Language: p.codeLanguage,
			Filename: p.codeFilename,
		})
		p.inCodeBlock = false
		p.codeLanguage = ""
		p.codeFilename = ""
		p.codeContent.Reset()
	}

	return blocks
}

// Reset clears the parser state.
func (p *MarkdownStreamParser) Reset() {
	p.buffer.Reset()
	p.inCodeBlock = false
	p.codeLanguage = ""
	p.codeFilename = ""
	p.codeContent.Reset()
	p.inTable = false
	p.tableRows = nil
}

// effectiveLanguage resolves the highlight language: explicit fence language
// first, then detection from the fence filename.
func (p *MarkdownStreamParser) effectiveLanguage(block RenderedBlock) string {
	if block.Language != "" {
		return block.Language
	}
	if block.Filename != "" {
		return p.highlighter.DetectLanguage(block.Filename)
	}
	return ""
}

// RenderCodeFenceTop renders a quiet dim language/filename label above the
// block — NO full-width rule. The 2-space indent of the code lines is the calm
// signal that this is a code block (CC uses just an indent + a small dim label).
// The two full-width `─` rules this used to draw were the same heavy weight the
// project removed as the inter-turn ruler, framing every block twice with a
// bold-violet label louder than the code itself.
func (p *MarkdownStreamParser) RenderCodeFenceTop(block RenderedBlock, width int) string {
	lang := p.effectiveLanguage(block)
	label := lang
	if block.Filename != "" {
		label = block.Filename
		if lang != "" {
			label = block.Filename + " · " + lang
		}
	}
	if label == "" {
		return ""
	}
	label = safeInlineDisplayText(label)
	if width <= 0 {
		return ""
	}
	label = truncateForWidth(label, width)
	return p.styles.Dim.Render(label) + "\n"
}

// RenderCodeLine renders one streamed code line: 2-space indent, per-line
// syntax highlighting. Line-local highlighting can mis-color constructs that
// span lines (block comments, raw strings) — an accepted trade for code that
// appears AS it streams instead of after the closing fence.
func (p *MarkdownStreamParser) RenderCodeLine(block RenderedBlock) string {
	// Tabs rendered by a terminal depend on the current absolute column, while
	// viewport wrapping measures the string independently. Expand them before
	// highlighting so code alignment and wrapping use the same four-cell stops.
	line := expandDisplayTabs(safeTerminalDisplayText(block.Content), 4)
	line = strings.ReplaceAll(line, "\n", " ")
	if lang := p.effectiveLanguage(block); lang != "" && strings.TrimSpace(line) != "" {
		line = strings.TrimSuffix(p.highlighter.Highlight(line, lang), "\n")
	}
	return "  " + line + "\n"
}

// RenderCodeFenceBottom emits nothing — the indent + the blank line after the
// block already delimit it (CC has no bottom rule).
func (p *MarkdownStreamParser) RenderCodeFenceBottom(width int) string {
	return ""
}

// renderInlineSegments renders backtick spans atomically and applies renderText
// only to the surrounding prose. Keeping code out of later Markdown regexes is
// important: `**literal**` and `[x](y)` inside code are content, not formatting.
func (p *MarkdownStreamParser) renderInlineSegments(line string, renderText func(string) string) string {
	if !strings.Contains(line, "`") {
		return renderText(line)
	}

	var result strings.Builder
	remaining := line
	for {
		start, contentStart, contentEnd, end, ok := nextInlineCodeSpan(remaining)
		if !ok {
			result.WriteString(renderText(remaining))
			break
		}

		result.WriteString(renderText(remaining[:start]))
		code := remaining[contentStart:contentEnd]
		// CommonMark trims one padding space when both sides have one and the
		// span is not entirely whitespace. This lets `` `literal` `` read as
		// `literal` while preserving intentional internal spacing.
		if strings.HasPrefix(code, " ") && strings.HasSuffix(code, " ") && strings.TrimSpace(code) != "" {
			code = code[1 : len(code)-1]
		}
		result.WriteString(p.styles.InlineCode.Render(code))
		remaining = remaining[end:]
	}
	return result.String()
}

// nextInlineCodeSpan finds matching backtick runs, including double-backtick
// spans whose contents may contain a literal single backtick. Byte indexes are
// safe here because every delimiter byte is ASCII and therefore a UTF-8 boundary.
func nextInlineCodeSpan(text string) (start, contentStart, contentEnd, end int, ok bool) {
	for search := 0; search < len(text); {
		rel := strings.IndexByte(text[search:], '`')
		if rel < 0 {
			break
		}
		open := search + rel
		openEnd := open
		for openEnd < len(text) && text[openEnd] == '`' {
			openEnd++
		}
		delimiterLen := openEnd - open

		for closeSearch := openEnd; closeSearch < len(text); {
			closeRel := strings.IndexByte(text[closeSearch:], '`')
			if closeRel < 0 {
				break
			}
			close := closeSearch + closeRel
			closeEnd := close
			for closeEnd < len(text) && text[closeEnd] == '`' {
				closeEnd++
			}
			if closeEnd-close == delimiterLen && close > openEnd {
				return open, openEnd, close, closeEnd, true
			}
			closeSearch = closeEnd
		}

		search = openEnd
	}
	return 0, 0, 0, 0, false
}

// renderListItem detects unordered (-, *) and ordered (1.) list items,
// calculates nesting depth from leading whitespace, and renders with
// proper indentation and bullet/number styling.
// Returns the rendered string and true if the line is a list item, or ("", false) otherwise.
func (p *MarkdownStreamParser) renderListItem(line string) (string, bool) {
	// One calm dim bullet at every depth — the indent conveys nesting, so the
	// marker can recede. (The old code cycled 6 bold-cyan geometric glyphs by
	// depth — the same "glyph confetti" already removed from tool lines.)
	bulletStyle := lipgloss.NewStyle().Foreground(ColorDim)
	numberStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

	// Check for unordered list: "  - item" or "  * item"
	if matches := unorderedListRegex.FindStringSubmatch(line); matches != nil {
		indent := matches[1]
		content := matches[3]
		depth := listDepth(indent)

		// Build indentation: 2 spaces per depth level.
		prefix := strings.Repeat("  ", depth)

		rendered := prefix + bulletStyle.Render("•") + " " + p.renderInlineFormatting(content)
		return rendered, true
	}

	// Check for ordered list: "  1. item"
	if matches := orderedListRegex.FindStringSubmatch(line); matches != nil {
		indent := matches[1]
		num := matches[2]
		content := matches[3]
		depth := listDepth(indent)

		// Build indentation: 2 spaces per depth level
		prefix := strings.Repeat("  ", depth)

		rendered := prefix + numberStyle.Render(num+".") + " " + p.renderInlineFormatting(content)
		return rendered, true
	}

	return "", false
}

// renderMarkdownLine renders a single line of markdown with full formatting support:
// headings, blockquotes, horizontal rules, lists, and inline formatting (bold, italic, etc.)
func (p *MarkdownStreamParser) renderMarkdownLine(line string) string {
	// Feed already sanitizes ordinary streaming input, but keep this renderer a
	// safe boundary as well because tests and non-stream callers use it directly.
	line = strings.ReplaceAll(safeTerminalDisplayText(line), "\n", " ")
	trimmed := strings.TrimSpace(line)

	// Horizontal rules: ---, ***, ___
	if horizontalRuleRegex.MatchString(trimmed) {
		hrStyle := lipgloss.NewStyle().Foreground(ColorDim)
		width := 40
		if p.renderWidth > 0 {
			width = min(width, p.renderWidth)
		}
		return hrStyle.Render(strings.Repeat("─", max(width, 1)))
	}

	// Headings: # H1, ## H2, etc.
	if matches := headingRegex.FindStringSubmatch(trimmed); matches != nil {
		level := len(matches[1])
		text := matches[2]
		headingStyle := lipgloss.NewStyle().Bold(true)
		// Color by heading level
		switch level {
		case 1:
			headingStyle = headingStyle.Foreground(ColorAccent)
		case 2:
			headingStyle = headingStyle.Foreground(ColorSecondary)
		case 3:
			headingStyle = headingStyle.Foreground(ColorInfo)
		default:
			headingStyle = headingStyle.Foreground(ColorText)
		}
		// Render the heading as styled (bold + per-level color) text WITHOUT the
		// literal #/##/### markup — the hashes are markup, not content, and the
		// bold+color already carries the hierarchy (CC renders headings this way).
		return p.renderInlineFormattingWithBase(text, headingStyle)
	}

	// Blockquotes: > text
	if matches := blockquoteRegex.FindStringSubmatch(line); matches != nil {
		quoteStyle := lipgloss.NewStyle().Foreground(ColorMuted)
		barStyle := lipgloss.NewStyle().Foreground(ColorDim)
		styledContent := p.renderInlineFormattingWithBase(matches[1], quoteStyle)
		return barStyle.Render("│ ") + styledContent
	}

	// List items
	if rendered, ok := p.renderListItem(line); ok {
		return rendered
	}

	// Regular text with full inline formatting
	return p.renderInlineFormatting(line)
}

// renderInlineFormatting applies bold, italic, strikethrough, links, and inline code.
func (p *MarkdownStreamParser) renderInlineFormatting(line string) string {
	return p.renderInlineFormattingWithBase(line, lipgloss.NewStyle().Foreground(ColorText))
}

func (p *MarkdownStreamParser) renderInlineFormattingWithBase(line string, base lipgloss.Style) string {
	if line == "" {
		return line
	}

	line = p.renderInlineSegments(line, renderPlainInlineFormatting)
	return applyBaseStyle(line, base)
}

// renderPlainInlineFormatting applies Markdown styling to prose known to be
// outside backtick code spans.
func renderPlainInlineFormatting(line string) string {

	// Bold: **text**
	boldStyle := lipgloss.NewStyle().Bold(true)
	line = boldRegex.ReplaceAllStringFunc(line, func(match string) string {
		inner := boldRegex.FindStringSubmatch(match)
		if len(inner) > 1 {
			return boldStyle.Render(inner[1])
		}
		return match
	})

	// Italic: *text* (but not **text**)
	italicStyle := lipgloss.NewStyle().Italic(true)
	line = italicRegex.ReplaceAllStringFunc(line, func(match string) string {
		inner := italicRegex.FindStringSubmatch(match)
		if len(inner) > 1 {
			// Reconstruct with surrounding context chars from lookaround
			prefix := ""
			suffix := ""
			if len(match) > 0 && match[0] != '*' {
				prefix = string(match[0])
			}
			if len(match) > 0 && match[len(match)-1] != '*' {
				suffix = string(match[len(match)-1])
			}
			return prefix + italicStyle.Render(inner[1]) + suffix
		}
		return match
	})

	// Strikethrough: ~~text~~
	strikeStyle := lipgloss.NewStyle().Strikethrough(true)
	line = strikethroughRegex.ReplaceAllStringFunc(line, func(match string) string {
		inner := strikethroughRegex.FindStringSubmatch(match)
		if len(inner) > 1 {
			return strikeStyle.Render(inner[1])
		}
		return match
	})

	// Links: [text](url)
	linkStyle := lipgloss.NewStyle().Foreground(ColorInfo).Underline(true)
	line = linkRegex.ReplaceAllStringFunc(line, func(match string) string {
		inner := linkRegex.FindStringSubmatch(match)
		if len(inner) > 2 {
			return inner[1] + " " + linkStyle.Render("("+inner[2]+")")
		}
		return match
	})

	return line
}

// listDepth calculates the nesting depth from leading whitespace.
// Each 2 spaces (or 1 tab) equals one level of depth.
func listDepth(indent string) int {
	spaces := 0
	for _, ch := range indent {
		if ch == '\t' {
			spaces += 2
		} else {
			spaces++
		}
	}
	return spaces / 2
}

// flushTable renders all buffered table rows as a formatted table block
// and resets the table state. Returns rendered blocks.
func (p *MarkdownStreamParser) flushTable() []RenderedBlock {
	if len(p.tableRows) == 0 {
		p.inTable = false
		return nil
	}

	rows := p.tableRows
	p.tableRows = nil
	p.inTable = false

	rendered := p.renderTable(rows)
	return []RenderedBlock{
		{
			Content: rendered + "\n",
			IsCode:  false,
		},
	}
}

// renderTable parses markdown table rows and renders them with aligned columns
// and box-drawing border characters.
func (p *MarkdownStreamParser) renderTable(rows []string) string {
	if len(rows) == 0 {
		return ""
	}

	// Parse all rows into cells. A plain strings.Split would mistake pipes in
	// `inline|code` and escaped \| characters for structural separators.
	var parsed []markdownTableRow
	for _, row := range rows {
		isSep := tableSeparatorRegex.MatchString(row)
		parsed = append(parsed, markdownTableRow{cells: splitMarkdownTableRow(row), isSeparator: isSep})
	}

	if len(parsed) == 0 {
		return strings.Join(rows, "\n")
	}

	// Determine column count and max widths
	maxCols := 0
	for _, r := range parsed {
		if len(r.cells) > maxCols {
			maxCols = len(r.cells)
		}
	}
	if maxCols == 0 {
		return strings.Join(rows, "\n")
	}

	borderStyle := p.styles.Dim
	headerStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)

	// Render cells once, then measure their actual terminal-cell widths. Rune
	// counts misalign CJK, emoji and combining marks; measuring raw Markdown also
	// counts syntax such as backticks that is not visible after rendering.
	renderedCells := make([][]string, len(parsed))
	isHeader := true
	for i, r := range parsed {
		if r.isSeparator {
			isHeader = false
			continue
		}
		renderedCells[i] = make([]string, len(r.cells))
		for j, cell := range r.cells {
			if isHeader {
				renderedCells[i][j] = p.renderInlineFormattingWithBase(cell, headerStyle)
			} else {
				renderedCells[i][j] = p.renderInlineFormatting(cell)
			}
		}
	}

	// Calculate the max visible width for each column.
	colWidths := make([]int, maxCols)
	for i, r := range parsed {
		if r.isSeparator {
			continue
		}
		for j, cell := range renderedCells[i] {
			w := lipgloss.Width(cell)
			if w > colWidths[j] {
				colWidths[j] = w
			}
		}
	}

	// Ensure minimum column width of 3
	for j := range colWidths {
		if colWidths[j] < 3 {
			colWidths[j] = 3
		}
	}

	// Keep boxed rows inside the viewport so OutputModel does not wrap each
	// border independently. When even one cell per column cannot fit, switch to
	// a vertical label/value layout that retains content and remains navigable.
	if p.renderWidth > 0 && tableBoxWidth(colWidths) > p.renderWidth {
		fitted := fitTableColumnWidths(colWidths, p.renderWidth)
		if fitted == nil {
			return p.renderStackedTable(parsed, renderedCells, p.renderWidth)
		}
		colWidths = fitted
	}

	var result strings.Builder

	// Top border: ╭───┬───┬───╮
	result.WriteString(borderStyle.Render("╭"))
	for j, w := range colWidths {
		result.WriteString(borderStyle.Render(strings.Repeat("─", w+2)))
		if j < maxCols-1 {
			result.WriteString(borderStyle.Render("┬"))
		}
	}
	result.WriteString(borderStyle.Render("╮"))
	result.WriteString("\n")

	// Render each row
	for rowIndex, r := range parsed {
		if r.isSeparator {
			// Separator row: ├───┼───┼───┤
			result.WriteString(borderStyle.Render("├"))
			for j, w := range colWidths {
				result.WriteString(borderStyle.Render(strings.Repeat("─", w+2)))
				if j < maxCols-1 {
					result.WriteString(borderStyle.Render("┼"))
				}
			}
			result.WriteString(borderStyle.Render("┤"))
			result.WriteString("\n")
			continue
		}

		// Data row: │ cell │ cell │ cell │
		result.WriteString(borderStyle.Render("│"))
		for j := 0; j < maxCols; j++ {
			styledCell := ""
			if j < len(renderedCells[rowIndex]) {
				styledCell = renderedCells[rowIndex][j]
			}
			styledCell = truncateForWidth(styledCell, colWidths[j])

			// Pad cell to column width
			cellWidth := lipgloss.Width(styledCell)
			padding := colWidths[j] - cellWidth
			if padding < 0 {
				padding = 0
			}

			result.WriteString(" ")
			result.WriteString(styledCell)
			result.WriteString(strings.Repeat(" ", padding))
			result.WriteString(" ")

			if j < maxCols-1 {
				result.WriteString(borderStyle.Render("│"))
			}
		}
		result.WriteString(borderStyle.Render("│"))
		result.WriteString("\n")
	}

	// Bottom border: ╰───┴───┴───╯
	result.WriteString(borderStyle.Render("╰"))
	for j, w := range colWidths {
		result.WriteString(borderStyle.Render(strings.Repeat("─", w+2)))
		if j < maxCols-1 {
			result.WriteString(borderStyle.Render("┴"))
		}
	}
	result.WriteString(borderStyle.Render("╯"))

	return result.String()
}

// splitMarkdownTableRow separates structural pipes while preserving escaped
// pipes and complete backtick spans. It intentionally keeps the backticks so
// the ordinary inline renderer can style the cell afterwards.
func splitMarkdownTableRow(row string) []string {
	row = strings.TrimSpace(row)
	var cells []string
	cell := make([]byte, 0, len(row))
	flush := func() {
		cells = append(cells, strings.TrimSpace(string(cell)))
		cell = cell[:0]
	}

	for i := 0; i < len(row); {
		if row[i] == '`' {
			start, _, _, end, ok := nextInlineCodeSpan(row[i:])
			if ok && start == 0 {
				cell = append(cell, row[i:i+end]...)
				i += end
				continue
			}
		}
		if row[i] == '|' {
			backslashes := 0
			for j := len(cell) - 1; j >= 0 && cell[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 1 {
				// One slash is Markdown escaping, not visible cell content.
				cell = cell[:len(cell)-1]
				cell = append(cell, '|')
				i++
				continue
			}
			flush()
			i++
			continue
		}
		cell = append(cell, row[i])
		i++
	}
	flush()

	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	if len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

func tableBoxWidth(columnWidths []int) int {
	width := 1 // left border
	for _, columnWidth := range columnWidths {
		width += max(columnWidth, 0) + 3 // two spaces plus separator/right border
	}
	return width
}

// fitTableColumnWidths uses a fair round-robin allocation. Every column keeps
// at least one cell, while short identifying columns reach their natural size
// before long prose columns consume the remaining terminal width.
func fitTableColumnWidths(desired []int, width int) []int {
	available := width - 3*len(desired) - 1
	if len(desired) == 0 || available < len(desired) {
		return nil
	}
	fitted := make([]int, len(desired))
	for i := range fitted {
		fitted[i] = 1
	}
	remaining := available - len(fitted)
	for remaining > 0 {
		progress := false
		for i := range fitted {
			if fitted[i] >= desired[i] {
				continue
			}
			fitted[i]++
			remaining--
			progress = true
			if remaining == 0 {
				break
			}
		}
		if !progress {
			break
		}
	}
	return fitted
}

// renderStackedTable is the readable fallback when box chrome alone would be
// wider than the viewport. Standard header/separator tables become repeated
// "Label: value" rows; malformed/header-only tables remain compact row lists.
func (p *MarkdownStreamParser) renderStackedTable(parsed []markdownTableRow, renderedCells [][]string, width int) string {
	width = max(width, 1)
	separatorIndex := -1
	for i, row := range parsed {
		if row.isSeparator {
			separatorIndex = i
			break
		}
	}

	headerIndex := -1
	if separatorIndex > 0 {
		for i := separatorIndex - 1; i >= 0; i-- {
			if !parsed[i].isSeparator {
				headerIndex = i
				break
			}
		}
	}

	var lines []string
	if headerIndex >= 0 && separatorIndex+1 < len(parsed) {
		for rowIndex := separatorIndex + 1; rowIndex < len(parsed); rowIndex++ {
			if parsed[rowIndex].isSeparator {
				continue
			}
			maxCols := max(len(renderedCells[headerIndex]), len(renderedCells[rowIndex]))
			for column := 0; column < maxCols; column++ {
				label := ""
				if column < len(renderedCells[headerIndex]) {
					label = strings.TrimSpace(ansi.Strip(renderedCells[headerIndex][column]))
				}
				value := ""
				if column < len(renderedCells[rowIndex]) {
					value = renderedCells[rowIndex][column]
				}
				lines = append(lines, p.renderStackedTableCell(label, value, width))
			}
		}
	} else {
		for rowIndex, row := range parsed {
			if row.isSeparator {
				continue
			}
			lines = append(lines, truncateForWidth(strings.Join(renderedCells[rowIndex], " · "), width))
		}
	}
	return strings.Join(lines, "\n")
}

func (p *MarkdownStreamParser) renderStackedTableCell(label, value string, width int) string {
	if strings.TrimSpace(ansi.Strip(value)) == "" {
		value = p.styles.Dim.Render("—")
	}
	if label == "" || width < 4 {
		return truncateForWidth(value, width)
	}

	// Reserve at least half the row for the value so a long header cannot hide
	// the only changing information in narrow layouts.
	valueReserve := max(width/2, 1)
	labelBudget := width - valueReserve - lipgloss.Width(": ")
	if labelBudget < 1 {
		return truncateForWidth(value, width)
	}
	label = truncateForWidth(label, labelBudget)
	prefix := p.styles.Dim.Render(label + ": ")
	value = truncateForWidth(value, max(width-lipgloss.Width(prefix), 1))
	return prefix + value
}

// applyBaseStyle ensures that plain text between styled spans has an explicit foreground color.
// Inner styled spans emit \x1b[0m (full reset) which strips the outer foreground.
// This function re-applies the base style's ANSI sequence after every reset.
func applyBaseStyle(text string, base lipgloss.Style) string {
	if text == "" {
		return text
	}
	// Render an empty string to extract the ANSI start sequence from the style
	rendered := base.Render("")
	idx := strings.Index(rendered, "\x1b[0m")
	if idx <= 0 {
		return text
	}
	baseSeq := rendered[:idx]
	// After every ANSI reset, re-apply the base style
	text = strings.ReplaceAll(text, "\x1b[0m", "\x1b[0m"+baseSeq)
	return baseSeq + text + "\x1b[0m"
}
