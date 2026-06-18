package ui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"gokin/internal/highlight"

	"github.com/charmbracelet/lipgloss"
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

	// Table buffering state: accumulate consecutive table lines before rendering
	inTable   bool
	tableRows []string
}

// codeBlockStartRegex matches code block start: ```lang or ```lang:filename
var codeBlockStartRegex = regexp.MustCompile("^```(\\w*)(?::(.+))?$")

// inlineCodeRegex matches single backtick-wrapped inline code: `code`
// It avoids matching double backticks (which start code blocks).
var inlineCodeRegex = regexp.MustCompile("`([^`]+)`")

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

// Feed processes a chunk of text and returns any completed blocks.
func (p *MarkdownStreamParser) Feed(chunk string) []RenderedBlock {
	var blocks []RenderedBlock

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

	// Flush remaining buffer
	remaining := p.buffer.String()
	if remaining != "" {
		blocks = append(blocks, RenderedBlock{
			Content: p.renderInlineCode(remaining),
			IsCode:  false,
		})
		p.buffer.Reset()
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
	return p.styles.Dim.Render(label) + "\n"
}

// RenderCodeLine renders one streamed code line: 2-space indent, per-line
// syntax highlighting. Line-local highlighting can mis-color constructs that
// span lines (block comments, raw strings) — an accepted trade for code that
// appears AS it streams instead of after the closing fence.
func (p *MarkdownStreamParser) RenderCodeLine(block RenderedBlock) string {
	line := block.Content
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

// renderInlineCode detects backtick-wrapped `code` segments within a text line
// and applies a distinct background color + monospace style using lipgloss.
// Double/triple backticks are not matched (those are code block fences).
func (p *MarkdownStreamParser) renderInlineCode(line string) string {
	if !strings.Contains(line, "`") {
		return line
	}

	var result strings.Builder
	remaining := line

	for {
		loc := inlineCodeRegex.FindStringIndex(remaining)
		if loc == nil {
			result.WriteString(remaining)
			break
		}

		// Write text before the match
		result.WriteString(remaining[:loc[0]])

		// Extract the matched segment and the inner code text
		matched := remaining[loc[0]:loc[1]]
		// Strip the surrounding backticks to get inner text
		inner := matched[1 : len(matched)-1]

		// Apply inline code style
		result.WriteString(p.styles.InlineCode.Render(inner))

		// Advance past the match
		remaining = remaining[loc[1]:]
	}

	return result.String()
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
	trimmed := strings.TrimSpace(line)

	// Horizontal rules: ---, ***, ___
	if horizontalRuleRegex.MatchString(trimmed) {
		hrStyle := lipgloss.NewStyle().Foreground(ColorDim)
		return hrStyle.Render(strings.Repeat("─", 40))
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
		cleanText := stripMarkdownMarkers(text)
		styledText := p.renderInlineCode(cleanText)
		return applyBaseStyle(styledText, headingStyle)
	}

	// Blockquotes: > text
	if matches := blockquoteRegex.FindStringSubmatch(line); matches != nil {
		quoteStyle := lipgloss.NewStyle().Foreground(ColorMuted)
		barStyle := lipgloss.NewStyle().Foreground(ColorDim)
		styledContent := p.renderInlineCode(matches[1])
		cleanContent := stripMarkdownMarkers(styledContent)
		return barStyle.Render("│ ") + applyBaseStyle(cleanContent, quoteStyle)
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
	if line == "" {
		return line
	}

	// Apply inline code first (so code content isn't processed for other formatting)
	line = p.renderInlineCode(line)

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

	// Ensure all plain text has explicit foreground color
	return applyBaseStyle(line, lipgloss.NewStyle().Foreground(ColorText))
}

// stripMarkdownMarkers removes bold and strikethrough markers from text.
// Used for headings/blockquotes where the parent style already provides formatting,
// avoiding nested ANSI sequences that break foreground colors.
func stripMarkdownMarkers(text string) string {
	text = boldRegex.ReplaceAllString(text, "$1")
	text = strikethroughRegex.ReplaceAllString(text, "$1")
	return text
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

	// Parse all rows into cells
	type parsedRow struct {
		cells       []string
		isSeparator bool
	}

	var parsed []parsedRow
	for _, row := range rows {
		trimmed := strings.TrimSpace(row)
		isSep := tableSeparatorRegex.MatchString(row)

		// Split on | and trim the outer empty cells
		parts := strings.Split(trimmed, "|")
		var cells []string
		for i, part := range parts {
			// Skip empty leading/trailing parts from outer pipes
			if (i == 0 || i == len(parts)-1) && strings.TrimSpace(part) == "" {
				continue
			}
			cells = append(cells, strings.TrimSpace(part))
		}

		parsed = append(parsed, parsedRow{cells: cells, isSeparator: isSep})
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

	// Calculate the max visible width for each column
	colWidths := make([]int, maxCols)
	for _, r := range parsed {
		if r.isSeparator {
			continue
		}
		for j, cell := range r.cells {
			w := utf8.RuneCountInString(cell)
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

	borderStyle := p.styles.Dim
	headerStyle := lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true)

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
	isHeader := true // First non-separator row is the header
	for _, r := range parsed {
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
			isHeader = false
			continue
		}

		// Data row: │ cell │ cell │ cell │
		result.WriteString(borderStyle.Render("│"))
		for j := 0; j < maxCols; j++ {
			cell := ""
			if j < len(r.cells) {
				cell = r.cells[j]
			}

			// Pad cell to column width
			cellWidth := utf8.RuneCountInString(cell)
			padding := colWidths[j] - cellWidth
			if padding < 0 {
				padding = 0
			}

			var styledCell string
			if isHeader {
				styledCell = headerStyle.Render(cell)
			} else {
				styledCell = p.renderInlineCode(cell)
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
