package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestComposerUpNavigatesMultilineDraftBeforeRecallingHistory(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"older message"})
	m.textarea.SetValue("first\nsecond")
	m.textarea.CursorEnd()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})

	if got, want := m.textarea.Value(), "firstX\nsecond"; got != want {
		t.Fatalf("Up replaced or failed to navigate the multiline draft: got %q, want %q", got, want)
	}
}

func TestComposerDownNavigatesMultilineDraftBeforeReturningToNewerHistory(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"older message"})
	m.textarea.SetValue("first\nsecond")
	m.textarea.CursorUp()
	m.textarea.CursorStart()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})

	if got, want := m.textarea.Value(), "first\nXsecond"; got != want {
		t.Fatalf("Down failed to navigate the multiline draft: got %q, want %q", got, want)
	}
}

func TestComposerArrowsRespectSoftWrappedVisualBoundaries(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(12)
	m.SetHistory([]string{"older message"})
	draft := "abcdefghijklmnop"
	m.textarea.SetValue(draft)
	m.textarea.CursorEnd()
	lastRow := m.textarea.LineInfo().RowOffset

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.textarea.Value(); got != draft || m.historyIndex != -1 || m.textarea.LineInfo().RowOffset >= lastRow {
		t.Fatalf("Up on a lower soft-wrapped row did not navigate visually: value=%q index=%d row=%d", got, m.historyIndex, m.textarea.LineInfo().RowOffset)
	}

	m.textarea.CursorStart()
	firstRow := m.textarea.LineInfo().RowOffset
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.textarea.Value(); got != draft || m.historyIndex != -1 || m.textarea.LineInfo().RowOffset <= firstRow {
		t.Fatalf("Down on an upper soft-wrapped row did not navigate visually: value=%q index=%d row=%d", got, m.historyIndex, m.textarea.LineInfo().RowOffset)
	}
}

func TestComposerHistoryNavigationActivatesAtOuterVisualBoundaries(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"older message"})
	draft := "first\nsecond"
	m.textarea.SetValue(draft)
	m.textarea.CursorUp()
	m.textarea.CursorStart()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.textarea.Value(); got != "older message" || m.historyIndex != 0 {
		t.Fatalf("Up at the first visual boundary did not recall history: value=%q index=%d", got, m.historyIndex)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.textarea.Value(); got != draft || m.historyIndex != -1 {
		t.Fatalf("Down at the last visual boundary did not restore the draft: value=%q index=%d", got, m.historyIndex)
	}
}

func TestHistoryRecallClearsFeedbackForTheReplacedDraft(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(64)
	m.SetHistory([]string{"recalled message"})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/definitely-missing")})
	if m.suggestionNotice == "" {
		t.Fatal("setup did not produce no-match feedback")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.textarea.Value(); got != "recalled message" {
		t.Fatalf("history recall = %q, want recalled message", got)
	}
	if m.suggestionNotice != "" || strings.Contains(stripAnsi(m.View()), "No commands match") {
		t.Fatalf("history recall retained feedback for the replaced draft: notice=%q\n%s", m.suggestionNotice, stripAnsi(m.View()))
	}
}

func TestHistorySearchBackspaceDeletesOneGraphemeCluster(t *testing.T) {
	for _, cluster := range []string{"👍🏽", "e\u0301", "👨‍👩‍👧‍👦"} {
		t.Run(cluster, func(t *testing.T) {
			m := NewInputModel(DefaultStyles(), t.TempDir())
			m.SetHistory([]string{"previous message"})
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(cluster)})

			m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
			if got := m.historySearchQuery; got != "" {
				t.Fatalf("Backspace split one visible grapheme into %q", got)
			}
		})
	}
}

func TestHistorySearchCtrlHUsesBackspaceGraphemeSemantics(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"previous message"})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("👍🏽")})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	if got := m.historySearchQuery; got != "" {
		t.Fatalf("Ctrl+H split one history-search grapheme into %q", got)
	}
}

func TestComposerBackspaceDeletesOneGraphemeAtEndAndMiddle(t *testing.T) {
	for _, cluster := range []string{"👍🏽", "e\u0301", "👨‍👩‍👧‍👦"} {
		for _, position := range []string{"end", "middle"} {
			t.Run(position+"/"+cluster, func(t *testing.T) {
				m := NewInputModel(DefaultStyles(), t.TempDir())
				if position == "end" {
					m.textarea.SetValue(cluster)
				} else {
					m.textarea.SetValue("A" + cluster + "B")
					m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
				}
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
				want := ""
				if position == "middle" {
					want = "AB"
				}
				if got := m.textarea.Value(); got != want {
					t.Fatalf("Backspace split grapheme: got %q, want %q", got, want)
				}
			})
		}
	}
}

func TestComposerGraphemeDeleteWithCaretInsideCluster(t *testing.T) {
	for _, cluster := range []string{"👍🏽", "e\u0301", "👨‍👩‍👧‍👦"} {
		for _, deletion := range []struct {
			name string
			key  tea.KeyMsg
		}{
			{name: "backspace", key: tea.KeyMsg{Type: tea.KeyBackspace}},
			{name: "ctrl+h", key: tea.KeyMsg{Type: tea.KeyCtrlH}},
			{name: "delete", key: tea.KeyMsg{Type: tea.KeyDelete}},
			{name: "ctrl+d", key: tea.KeyMsg{Type: tea.KeyCtrlD}},
		} {
			t.Run(deletion.name+"/"+cluster, func(t *testing.T) {
				m := NewInputModel(DefaultStyles(), t.TempDir())
				m.textarea.SetValue("A" + cluster + "B")
				// First Left lands after the EGC; the second lands inside it,
				// immediately before its final rune.
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
				m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
				m, _ = m.Update(deletion.key)
				if got := m.textarea.Value(); got != "AB" {
					t.Fatalf("inside-caret %s split grapheme: got %q", deletion.name, got)
				}
			})
		}
	}
}

func TestComposerForwardDeleteKeepsBoundaryAndEndSemantics(t *testing.T) {
	for _, keyMsg := range []tea.KeyMsg{{Type: tea.KeyDelete}, {Type: tea.KeyCtrlD}} {
		m := NewInputModel(DefaultStyles(), t.TempDir())
		m.textarea.SetValue("A👍🏽B")
		m.textarea.CursorStart()
		m.textarea.SetCursor(1)
		m, _ = m.Update(keyMsg)
		if got := m.textarea.Value(); got != "AB" {
			t.Fatalf("forward delete at EGC boundary = %q, want AB", got)
		}

		m.textarea.CursorEnd()
		m, _ = m.Update(keyMsg)
		if got := m.textarea.Value(); got != "AB" {
			t.Fatalf("forward delete at end changed input: %q", got)
		}
	}
}

func TestDecisionEditorBackspaceDeletesOneGraphemeAtEndAndMiddle(t *testing.T) {
	for _, cluster := range []string{"👍🏽", "e\u0301", "👨‍👩‍👧‍👦"} {
		for _, position := range []string{"end", "middle"} {
			t.Run(position+"/"+cluster, func(t *testing.T) {
				m := NewInputModel(DefaultStyles(), t.TempDir())
				if position == "end" {
					m.textarea.SetValue(cluster)
				} else {
					m.textarea.SetValue("A" + cluster + "B")
					m.textarea.CursorEnd()
					m, _ = m.updateDecisionEditor(tea.KeyMsg{Type: tea.KeyLeft})
				}
				m, _ = m.updateDecisionEditor(tea.KeyMsg{Type: tea.KeyBackspace})
				want := ""
				if position == "middle" {
					want = "AB"
				}
				if got := m.textarea.Value(); got != want {
					t.Fatalf("decision editor Backspace split grapheme: got %q, want %q", got, want)
				}
			})
		}
	}
}

func TestDecisionEditorGraphemeDeleteWithCaretInsideCluster(t *testing.T) {
	for _, cluster := range []string{"👍🏽", "e\u0301", "👨‍👩‍👧‍👦"} {
		for _, deletion := range []struct {
			name string
			key  tea.KeyMsg
		}{
			{name: "backspace", key: tea.KeyMsg{Type: tea.KeyBackspace}},
			{name: "delete", key: tea.KeyMsg{Type: tea.KeyDelete}},
			{name: "ctrl+d", key: tea.KeyMsg{Type: tea.KeyCtrlD}},
		} {
			t.Run(deletion.name+"/"+cluster, func(t *testing.T) {
				m := NewInputModel(DefaultStyles(), t.TempDir())
				m.textarea.SetValue("A" + cluster + "B")
				m, _ = m.updateDecisionEditor(tea.KeyMsg{Type: tea.KeyLeft})
				m, _ = m.updateDecisionEditor(tea.KeyMsg{Type: tea.KeyLeft})
				m, _ = m.updateDecisionEditor(deletion.key)
				if got := m.textarea.Value(); got != "AB" {
					t.Fatalf("decision editor inside-caret %s split grapheme: got %q", deletion.name, got)
				}
			})
		}
	}
}

func TestLiteralPasteChipTextDoesNotAliasStoredPaste(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	pasted := "first\nsecond\nthird\nfourth"
	m = m.collapsePaste(pasted)
	chip := pasteDisplayLabel(1, pasted)
	if got := m.Value(); got != chip {
		t.Fatalf("Value exposed anything except the visible paste label: %q", got)
	}
	if got := m.ExpandedValue(); got != pasted {
		t.Fatalf("ExpandedValue before literal text = %q, want payload", got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" literal " + chip)})

	want := pasted + " literal " + chip
	if got := m.ExpandedValue(); got != want {
		t.Fatalf("literal chip-shaped text aliased a stored paste:\ngot:  %q\nwant: %q", got, want)
	}
	plainView := stripAnsi(m.View())
	if !strings.Contains(plainView, chip) || strings.Contains(plainView, pasted) {
		t.Fatalf("paste identity or hidden payload leaked into View:\n%s", plainView)
	}
}

func TestPasteSpanTracksEditsBeforeAndAfterTheChip(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	pasted := "first\nsecond\nthird\nfourth"
	m = m.collapsePaste(pasted)
	m.textarea.CursorStart()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prefix ")})
	m.textarea.CursorEnd()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" suffix")})

	if got, want := m.ExpandedValue(), "prefix "+pasted+" suffix"; got != want {
		t.Fatalf("paste span lost identity after surrounding edits: got=%q want=%q spans=%v", got, want, m.pasteSpans)
	}
}

func TestDistinctPasteSpansExpandOnlyTheirOwnVisibleChips(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	first := "a\nb\nc\nd"
	second := "w\nx\ny\nz"
	m = m.collapsePaste(first)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" between ")})
	m = m.collapsePaste(second)

	if got, want := m.ExpandedValue(), first+" between "+second; got != want {
		t.Fatalf("distinct paste spans crossed identities:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestEditedOrDeletedPasteChipNeverExpandsHiddenContent(t *testing.T) {
	pasted := "first\nsecond\nthird\nfourth"

	edited := NewInputModel(DefaultStyles(), t.TempDir())
	edited = edited.collapsePaste(pasted)
	edited, _ = edited.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got, want := edited.ExpandedValue(), strings.TrimSpace(edited.textarea.Value()); got != want || strings.Contains(got, pasted) {
		t.Fatalf("partially edited chip expanded hidden content: got=%q want=%q", got, want)
	}

	deleted := NewInputModel(DefaultStyles(), t.TempDir())
	deleted = deleted.collapsePaste(pasted)
	deleted, _ = deleted.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := deleted.ExpandedValue(); got != "" {
		t.Fatalf("deleted chip still expanded hidden content: %q", got)
	}
}

func TestForwardDeleteInsidePasteLabelInvalidatesExpansion(t *testing.T) {
	pasted := "first\nsecond\nthird\nfourth"
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m = m.collapsePaste(pasted)
	m.textarea.CursorStart()
	m.textarea.SetCursor(5)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDelete})

	if got := m.ExpandedValue(); strings.Contains(got, pasted) || len(m.pasteSpans) != 0 {
		t.Fatalf("forward edit inside paste label retained expansion: expanded=%q spans=%v", got, m.pasteSpans)
	}
}

func TestPasteSpanIdentityDoesNotLeakIntoSubmitOrHistory(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput
	m.minSubmitDelay = 0
	pasted := "first\nsecond\nthird\nfourth"
	m.input = m.input.collapsePaste(pasted)
	label := pasteDisplayLabel(1, pasted)
	updatedInput, _ := m.input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" literal " + label)})
	m.input = updatedInput

	var submitted string
	m.SetCallbacks(func(value string) { submitted = value }, nil)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	want := pasted + " literal " + label
	if submitted != want {
		t.Fatalf("submitted paste identity leaked or aliased: got=%q want=%q", submitted, want)
	}
	if len(got.input.history) != 1 || got.input.history[0] != want {
		t.Fatalf("history did not store exact expanded submission: %#v", got.input.history)
	}
	if got.input.Value() != "" || len(got.input.pasteSpans) != 0 {
		t.Fatalf("submit retained paste identity: value=%q spans=%v", got.input.Value(), got.input.pasteSpans)
	}
}

func TestPasteSpanSurvivesHistoryDraftRoundTrip(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	pasted := "first\nsecond\nthird\nfourth"
	m = m.collapsePaste(pasted)
	visibleDraft := m.Value()
	m.SetHistory([]string{"older message"})
	m.textarea.CursorStart()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.Value(); got != "older message" {
		t.Fatalf("history recall = %q", got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.Value(); got != visibleDraft {
		t.Fatalf("history did not restore visible paste draft: got=%q want=%q", got, visibleDraft)
	}
	if got := m.ExpandedValue(); got != pasted {
		t.Fatalf("restored draft lost paste identity: got=%q want=%q spans=%v", got, pasted, m.pasteSpans)
	}
}
