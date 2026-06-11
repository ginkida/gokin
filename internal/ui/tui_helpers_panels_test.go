package ui

import (
	"strings"
	"testing"
)

func panelTestModel() Model {
	return Model{styles: DefaultStyles(), width: 100}
}

func TestRenderTodos_CollapsesDoneOnLongLists(t *testing.T) {
	m := panelTestModel()
	for i := 0; i < 9; i++ {
		m.todoItems = append(m.todoItems, "- [x] finished step")
	}
	m.todoItems = append(m.todoItems, "- [/] current step", "- [ ] next step")

	out := renderToPlain(m.renderTodos())
	if strings.Count(out, "finished step") != 0 {
		t.Fatalf("long list must collapse completed items:\n%s", out)
	}
	if !strings.Contains(out, "✓ 9 completed") {
		t.Fatalf("collapsed summary missing:\n%s", out)
	}
	for _, needle := range []string{"current step", "next step"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("active/pending rows must stay visible, missing %q:\n%s", needle, out)
		}
	}
}

func TestRenderTodos_ShortListsRenderFully(t *testing.T) {
	m := panelTestModel()
	m.todoItems = []string{"- [x] one", "- [/] two", "- [ ] three"}

	out := renderToPlain(m.renderTodos())
	for _, needle := range []string{"one", "two", "three"} {
		if !strings.Contains(out, needle) {
			t.Fatalf("short list must render every item, missing %q:\n%s", needle, out)
		}
	}
	if strings.Contains(out, "completed") {
		t.Fatalf("short list must not collapse:\n%s", out)
	}
}

func TestRenderScratchpad_CapsToTail(t *testing.T) {
	m := panelTestModel()
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, "note line")
	}
	lines = append(lines, "latest note")
	m.scratchpad = strings.Join(lines, "\n")

	out := renderToPlain(m.renderScratchpad())
	if !strings.Contains(out, "latest note") {
		t.Fatalf("scratchpad tail missing:\n%s", out)
	}
	if !strings.Contains(out, "earlier line") {
		t.Fatalf("hidden-lines marker missing:\n%s", out)
	}
	if got := strings.Count(out, "note line"); got > scratchpadMaxRenderLines {
		t.Fatalf("scratchpad rendered %d body lines, want ≤%d:\n%s", got, scratchpadMaxRenderLines, out)
	}
}

// renderToPlain strips ANSI escape sequences so assertions match content.
func renderToPlain(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && (r == 'm' || r == 'K'):
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}
