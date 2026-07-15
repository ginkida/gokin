package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func collectKinds(blocks []RenderedBlock) []RenderedBlockKind {
	kinds := make([]RenderedBlockKind, 0, len(blocks))
	for _, b := range blocks {
		kinds = append(kinds, b.Kind)
	}
	return kinds
}

func TestFeed_CodeBlockStreamsIncrementally(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())

	// Fence opens and TWO code lines arrive — with the fence still open,
	// both lines must already be emitted (the pre-incremental parser held
	// everything until the closing fence).
	blocks := p.Feed("```go\nfunc main() {\n\tprintln(1)\n")
	kinds := collectKinds(blocks)
	if len(kinds) != 3 || kinds[0] != BlockCodeStart || kinds[1] != BlockCodeLine || kinds[2] != BlockCodeLine {
		t.Fatalf("kinds = %v, want [start, line, line] before the fence closes", kinds)
	}
	if blocks[1].Content != "func main() {" {
		t.Fatalf("first code line = %q", blocks[1].Content)
	}
	if blocks[0].Language != "go" {
		t.Fatalf("start language = %q, want go", blocks[0].Language)
	}

	// Closing fence → end marker carrying the FULL content for the registry.
	endBlocks := p.Feed("}\n```\n")
	endKinds := collectKinds(endBlocks)
	if len(endKinds) != 2 || endKinds[0] != BlockCodeLine || endKinds[1] != BlockCodeEnd {
		t.Fatalf("end kinds = %v, want [line, end]", endKinds)
	}
	full := endBlocks[1]
	if !full.IsCode || full.Content != "func main() {\n\tprintln(1)\n}" {
		t.Fatalf("end block content = %q (IsCode=%v)", full.Content, full.IsCode)
	}
}

func TestFeed_PartialLineStaysBuffered(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	p.Feed("```go\n")
	// No newline yet — the incomplete code line must NOT be emitted.
	blocks := p.Feed("func ma")
	for _, b := range blocks {
		if b.Kind == BlockCodeLine {
			t.Fatalf("incomplete line emitted: %q", b.Content)
		}
	}
	blocks = p.Feed("in() {}\n")
	if len(blocks) != 1 || blocks[0].Kind != BlockCodeLine || blocks[0].Content != "func main() {}" {
		t.Fatalf("completed line blocks = %+v", blocks)
	}
}

func TestFlush_UnclosedFenceEmitsEndMarker(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	p.Feed("```python\nx = 1\n")
	blocks := p.Flush()
	if len(blocks) != 1 || blocks[0].Kind != BlockCodeEnd {
		t.Fatalf("flush blocks = %+v, want single end marker", blocks)
	}
	if blocks[0].Content != "x = 1" {
		t.Fatalf("flush end content = %q", blocks[0].Content)
	}
}

func TestFlush_FinalCodeLineWithoutNewlineStaysInFence(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	p.Feed("```go\n")
	p.Feed("func final() {}")

	blocks := p.Flush()
	if len(blocks) != 2 || blocks[0].Kind != BlockCodeLine || blocks[1].Kind != BlockCodeEnd {
		t.Fatalf("flush blocks = %+v, want final code line then end marker", blocks)
	}
	if blocks[0].Content != "func final() {}" || blocks[1].Content != "func final() {}" {
		t.Fatalf("final code line escaped fence or copy content: %+v", blocks)
	}
}

func TestOutputModel_CodeVisibleBeforeFenceCloses(t *testing.T) {
	m := NewOutputModel(DefaultStyles())
	m.SetSize(100, 30)

	m.AppendTextStream("```go\nfunc visible() {}\n")
	content := renderToPlain(m.Content())
	if !strings.Contains(content, "func visible() {}") {
		t.Fatalf("code line must be visible BEFORE the closing fence:\n%s", content)
	}

	m.AppendTextStream("```\n")
	closed := renderToPlain(m.Content())
	// One body occurrence — the end marker must not re-render the lines.
	if got := strings.Count(closed, "func visible() {}"); got != 1 {
		t.Fatalf("code body rendered %d times after close, want 1:\n%s", got, closed)
	}
}

func TestOutputModelFlushRegistersFinalCodeLineWithoutNewline(t *testing.T) {
	m := NewOutputModel(DefaultStyles())
	m.SetSize(80, 20)
	m.AppendTextStream("```go\nfunc final() {}")
	m.FlushStream()

	selected := m.GetCodeBlocks().GetSelected()
	if selected == nil || selected.Content != "func final() {}" {
		t.Fatalf("final unterminated code was not copyable: %+v", selected)
	}
	if plain := renderToPlain(m.Content()); strings.Count(plain, "func final() {}") != 1 {
		t.Fatalf("final code line should render exactly once:\n%s", plain)
	}
}

func TestFlush_FinalLineGetsFullMarkdownFormatting(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	if blocks := p.Feed("## **Done** and [docs](https://example.com)"); len(blocks) != 0 {
		t.Fatalf("unterminated final line emitted before flush: %+v", blocks)
	}

	blocks := p.Flush()
	if len(blocks) != 1 || blocks[0].Kind != BlockText {
		t.Fatalf("flush blocks = %+v, want one text block", blocks)
	}
	plain := stripAnsi(blocks[0].Content)
	for _, want := range []string{"Done", "docs", "(https://example.com)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("final Markdown line lost %q: %q", want, plain)
		}
	}
	for _, marker := range []string{"##", "**", "](https://example.com)"} {
		if strings.Contains(plain, marker) {
			t.Fatalf("final Markdown line retained syntax %q: %q", marker, plain)
		}
	}
}

func TestInlineCodeIsAtomicAgainstOtherMarkdown(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	plain := stripAnsi(p.renderMarkdownLine("keep `**raw** ~~x~~ [y](z)` exact"))
	if !strings.Contains(plain, "**raw** ~~x~~ [y](z)") {
		t.Fatalf("Markdown inside inline code was reinterpreted: %q", plain)
	}
	if !strings.Contains(plain, "keep ") || !strings.Contains(plain, " exact") {
		t.Fatalf("surrounding prose was lost: %q", plain)
	}

	double := stripAnsi(p.renderMarkdownLine("use `` a`b **raw** `` now"))
	if !strings.Contains(double, "a`b **raw**") || strings.Contains(double, "``") {
		t.Fatalf("double-backtick code span was not kept atomic: %q", double)
	}
}

func TestFeedMakesAssistantTerminalControlsInertAcrossChunks(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	blocks := p.Feed("before\x1b[2Jafter\x1b]0;owned\a\n")
	if len(blocks) != 1 {
		t.Fatalf("blocks = %+v, want one text block", blocks)
	}
	raw := blocks[0].Content
	for _, control := range []string{"\x1b[2J", "\x1b]0;", "\a"} {
		if strings.Contains(raw, control) {
			t.Fatalf("assistant control %q reached terminal output: %q", control, raw)
		}
	}
	plain := stripAnsi(raw)
	if !strings.Contains(plain, "before") || !strings.Contains(plain, "after") {
		t.Fatalf("sanitization discarded surrounding content: %q", plain)
	}

	// A sequence split at ESC must also be inert: the following chunk may be
	// visible as text, but cannot regain an executable introducer.
	p.Reset()
	p.Feed("left\x1b")
	split := p.Feed("[2Jright\n")
	if len(split) != 1 || strings.Contains(split[0].Content, "\x1b[2J") {
		t.Fatalf("split control sequence survived: %+v", split)
	}
	if got := stripAnsi(split[0].Content); !strings.Contains(got, "left[2Jright") {
		t.Fatalf("split sanitization lost readable content: %q", got)
	}
}

func TestCodeRenderingFitsLabelAndExpandsTabsDeterministically(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	top := p.RenderCodeFenceTop(RenderedBlock{
		Language: "go",
		Filename: "very-long-界界界\x1b[2J.go",
	}, 12)
	if strings.Contains(top, "\x1b[2J") {
		t.Fatalf("code label retained terminal control: %q", top)
	}
	if got := lipgloss.Width(strings.TrimSuffix(top, "\n")); got > 12 {
		t.Fatalf("code label width = %d, want <= 12: %q", got, stripAnsi(top))
	}

	line := stripAnsi(p.RenderCodeLine(RenderedBlock{Content: "a\tb"}))
	if line != "  a   b\n" {
		t.Fatalf("tab-expanded code line = %q, want stable four-cell stops", line)
	}
}

func TestMarkdownTableAlignsByRenderedTerminalCells(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	table := p.renderTable([]string{
		"| **Name** | Mark |",
		"| --- | --- |",
		"| 界 | e\u0301 |",
		"| A | 👩‍💻 |",
	})
	lines := strings.Split(table, "\n")
	if len(lines) < 5 {
		t.Fatalf("table rendered too few rows: %q", stripAnsi(table))
	}
	wantWidth := lipgloss.Width(lines[0])
	for i, line := range lines {
		if got := lipgloss.Width(line); got != wantWidth {
			t.Fatalf("table row %d width = %d, want %d: %q", i, got, wantWidth, stripAnsi(line))
		}
	}
	if plain := stripAnsi(table); strings.Contains(plain, "**Name**") || !strings.Contains(plain, "Name") {
		t.Fatalf("table measured raw Markdown instead of rendered content: %q", plain)
	}
}

func TestFlush_FinalTableRowWithoutNewlineStaysInTable(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	blocks := p.Feed("| Name | Value |\n| --- | --- |\n| first | one |\n| final | 界 |")
	if len(blocks) != 0 {
		t.Fatalf("table emitted before flush: %+v", blocks)
	}

	blocks = p.Flush()
	if len(blocks) != 1 || blocks[0].Kind != BlockText {
		t.Fatalf("flush blocks = %+v, want one complete table", blocks)
	}
	plain := stripAnsi(blocks[0].Content)
	if !strings.Contains(plain, "first") || !strings.Contains(plain, "final") || strings.Contains(plain, "| final |") {
		t.Fatalf("final row was detached from rendered table: %q", plain)
	}
}

func TestMarkdownTableShrinksToCurrentTranscriptWidth(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	p.SetWidth(18)
	table := p.renderTable([]string{
		"| Long heading | Another heading |",
		"| --- | --- |",
		"| a very long value | another long value |",
	})

	for i, line := range strings.Split(table, "\n") {
		if got := lipgloss.Width(line); got > 18 {
			t.Fatalf("responsive table row %d width=%d, want <=18: %q", i, got, stripAnsi(line))
		}
	}
	plain := stripAnsi(table)
	if !strings.Contains(plain, "Long") || !strings.Contains(plain, "a ver") || !strings.Contains(plain, "…") {
		t.Fatalf("responsive table lost identity or truncation feedback: %q", plain)
	}
}

func TestMarkdownTableUsesStackedLabelsWhenBoxCannotFit(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	p.SetWidth(8) // Two boxed columns need at least nine cells of chrome/content.
	table := p.renderTable([]string{
		"| A | B |",
		"| --- | --- |",
		"| x | 界 |",
	})
	plain := stripAnsi(table)

	if strings.ContainsAny(plain, "╭┬╮│") {
		t.Fatalf("impossible narrow box did not switch to stacked layout: %q", plain)
	}
	for _, want := range []string{"A: x", "B: 界"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("stacked table lost %q: %q", want, plain)
		}
	}
	for i, line := range strings.Split(table, "\n") {
		if got := lipgloss.Width(line); got > 8 {
			t.Fatalf("stacked row %d width=%d, want <=8: %q", i, got, stripAnsi(line))
		}
	}
}

func TestMarkdownTableParserPreservesCodeAndEscapedPipes(t *testing.T) {
	got := splitMarkdownTableRow("| `a|b` | c\\|d | plain |")
	want := []string{"`a|b`", "c|d", "plain"}
	if len(got) != len(want) {
		t.Fatalf("cells=%q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cell %d=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestMarkdownHorizontalRuleUsesCurrentWidth(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	p.SetWidth(7)
	if got := lipgloss.Width(p.renderMarkdownLine("---")); got != 7 {
		t.Fatalf("horizontal rule width=%d, want 7", got)
	}
}

func TestOutputSizePropagatesResponsiveMarkdownWidth(t *testing.T) {
	output := NewOutputModel(DefaultStyles())
	output.SetSize(16, 12) // Transcript keeps a four-cell comfort margin => 12.
	output.AppendTextStream("| Heading one | Heading two |\n| --- | --- |\n| first value | second value |")
	output.FlushStream()

	for i, line := range strings.Split(output.Content(), "\n") {
		if got := lipgloss.Width(line); got > 12 {
			t.Fatalf("output table row %d width=%d, want <=12: %q", i, got, stripAnsi(line))
		}
	}
}
