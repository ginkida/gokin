package ui

import (
	"strings"
	"testing"
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
