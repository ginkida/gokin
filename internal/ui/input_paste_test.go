package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestIsLargePaste(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"short single line", "hello world", false},
		{"3 lines below threshold", "a\nb\nc", false}, // 2 newlines
		{"4 lines collapse", "a\nb\nc\nd", true},      // 3 newlines
		{"long single line collapse", strings.Repeat("x", 400), true},
		{"399-char single line below", strings.Repeat("x", 399), false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := isLargePaste(c.text); got != c.want {
			t.Errorf("isLargePaste(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCollapsePaste_RoundTrip: a large paste shows a compact chip in the input
// (Value, collapsed) and restores the exact content via ExpandedValue.
func TestCollapsePaste_RoundTrip(t *testing.T) {
	m := NewInputModel(DefaultStyles(), ".")
	big := "line1\nline2\nline3\nline4\nline5" // 5 lines

	m = m.collapsePaste(big)

	v := m.Value()
	if v != "[Pasted text #1 +5 lines]" {
		t.Fatalf("collapsed value should be the chip, got %q", v)
	}
	if strings.Contains(v, "line3") {
		t.Fatalf("collapsed value must NOT contain the raw paste: %q", v)
	}
	if exp := m.ExpandedValue(); exp != big {
		t.Fatalf("ExpandedValue = %q, want the original paste %q", exp, big)
	}
}

// TestCollapsePaste_MultipleAndCharsChip: numbered chips, "+N chars" for a long
// single line, and both pastes (plus typed text between) restored on expand.
func TestCollapsePaste_MultipleAndCharsChip(t *testing.T) {
	m := NewInputModel(DefaultStyles(), ".")
	longLine := strings.Repeat("a", 500) // single line → "+500 chars"
	m = m.collapsePaste(longLine)
	m.textarea.InsertString(" and ")
	multi := "x\ny\nz\nw" // 4 lines
	m = m.collapsePaste(multi)

	v := m.Value()
	if !strings.Contains(v, "[Pasted text #1 +500 chars]") {
		t.Fatalf("missing #1 chars chip: %q", v)
	}
	if !strings.Contains(v, "[Pasted text #2 +4 lines]") {
		t.Fatalf("missing #2 lines chip: %q", v)
	}
	exp := m.ExpandedValue()
	if !strings.Contains(exp, longLine) || !strings.Contains(exp, multi) {
		t.Fatalf("expanded must contain both pastes: %q", exp)
	}
	if !strings.Contains(exp, " and ") {
		t.Fatalf("expanded must keep the typed separator: %q", exp)
	}
}

// TestPaste_EndToEndViaUpdate: a large bracketed-paste KeyMsg collapses; a small
// one inserts verbatim.
func TestPaste_EndToEndViaUpdate(t *testing.T) {
	m := NewInputModel(DefaultStyles(), ".")
	big := strings.Repeat("z\n", 10) + "end" // 11 lines
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune(big)})
	if v := m.Value(); !strings.HasPrefix(v, "[Pasted text #1 +") {
		t.Fatalf("large paste KeyMsg should collapse to a chip, got %q", v)
	}
	if exp := m.ExpandedValue(); exp != big {
		t.Fatalf("expanded should restore the raw paste; got %q", exp)
	}

	m2 := NewInputModel(DefaultStyles(), ".")
	m2, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Paste: true, Runes: []rune("short paste")})
	if v := m2.Value(); v != "short paste" {
		t.Fatalf("small paste should insert verbatim, got %q", v)
	}
	if strings.Contains(m2.Value(), "Pasted text") {
		t.Fatalf("small paste must not collapse: %q", m2.Value())
	}
}

// TestPasteReset_ClearsStore: Reset drops the collapsed-paste content so the
// next compose starts fresh (chips renumber from #1).
func TestPasteReset_ClearsStore(t *testing.T) {
	m := NewInputModel(DefaultStyles(), ".")
	m = m.collapsePaste("a\nb\nc\nd\ne")
	m.Reset()
	if m.pastes != nil || m.pasteSeq != 0 {
		t.Fatalf("Reset must clear pastes: pastes=%v seq=%d", m.pastes, m.pasteSeq)
	}
}

// TestPaste_ClearsStaleSuggestions: collapsing a paste mid-suggestion clears the
// open dropdown/ghost state so the next Enter submits instead of accepting it.
func TestPaste_ClearsStaleSuggestions(t *testing.T) {
	m := NewInputModel(DefaultStyles(), ".")
	m.showSuggestions = true
	m.ghostText = "ghost"
	m = m.collapsePaste("a\nb\nc\nd\ne")
	if m.showSuggestions || m.ghostText != "" {
		t.Fatalf("paste collapse must clear suggestion/ghost state: show=%v ghost=%q", m.showSuggestions, m.ghostText)
	}
}

// TestExpandedValue_UnknownChipLeftAsIs: a chip the user TYPED (no stored
// content) is left verbatim — never silently dropped.
func TestExpandedValue_UnknownChipLeftAsIs(t *testing.T) {
	m := NewInputModel(DefaultStyles(), ".")
	m.textarea.InsertString("see [Pasted text #9 +3 lines] above")
	if exp := m.ExpandedValue(); exp != "see [Pasted text #9 +3 lines] above" {
		t.Fatalf("unknown chip should be left as-is, got %q", exp)
	}
}
