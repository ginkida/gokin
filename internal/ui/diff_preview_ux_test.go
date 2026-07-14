package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestDiffPreviewMakesControlsVisibleWithoutChangingApprovalPayload(t *testing.T) {
	rawPath := "dir/\x1b]0;hijacked-title\amain.go"
	rawOld := "before\n\x1b]0;old-title\aold\r\n"
	rawNew := "before\n\x1b]52;c;clipboard\aпривет 🙂\r\n"
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetSize(60, 20) // unified layout
	m.SetContent(rawPath, rawOld, rawNew, "\x1b]0;tool-title\aedit", false)

	view := m.View()
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b]") || strings.Contains(plain, "hijacked-title") || strings.Contains(plain, "tool-title") {
		t.Fatalf("diff preview retained executable metadata controls: %q", view)
	}
	for _, want := range []string{"␛]", "52;c;clipboard␇", "привет 🙂", "␍"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("diff preview hid proposed control data %q:\n%s", want, plain)
		}
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || updated.GetNewContent() != rawNew || updated.GetFilePath() != rawPath {
		t.Fatal("display safety changed raw approval data")
	}
	msg := cmd().(DiffPreviewResponseMsg)
	if msg.FilePath != rawPath || msg.NewContent != rawNew {
		t.Fatalf("approval payload changed: path=%q content=%q", msg.FilePath, msg.NewContent)
	}

	updated.SetSize(120, 24) // side-by-side uses the same safe display copy
	split := updated.viewport.View()
	if strings.Contains(split, "\x1b]") || !strings.Contains(stripAnsi(split), "␛]52;c;clipboard␇") {
		t.Fatalf("split preview did not preserve safe control notation: %q", split)
	}
}

func TestMultiDiffPreviewMakesControlsVisibleAndKeepsRawKeys(t *testing.T) {
	rawPath := "dir/\x1b]0;hijacked-title\amain.go"
	rawNew := "\x1b]52;c;clipboard\aновое 🙂\n"
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetFiles([]DiffFile{{FilePath: rawPath, OldContent: "old\n", NewContent: rawNew}})

	view := m.View()
	plain := stripAnsi(view)
	if strings.Contains(view, "\x1b]") || strings.Contains(plain, "hijacked-title") {
		t.Fatalf("multi diff retained executable controls: %q", view)
	}
	for _, want := range []string{"␛]52;c;clipboard␇", "новое 🙂"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("multi diff hid proposed control data %q:\n%s", want, plain)
		}
	}
	if m.files[0].FilePath != rawPath || m.files[0].NewContent != rawNew {
		t.Fatal("multi diff display safety changed raw file data")
	}
	if decisions := m.GetDecisions(); decisions[rawPath] != DiffPending {
		t.Fatalf("multi diff decision map lost raw path key: %v", decisions)
	}
}

func TestInlineDiffCardMakesProposedControlsVisible(t *testing.T) {
	card := renderInlineDiffCard(90, DiffPreviewRequestMsg{
		FilePath:   "dir/\x1b]0;hijacked-title\amain.go",
		ToolName:   "\x1b]0;tool-title\aedit",
		OldContent: "old\n",
		NewContent: "\x1b]52;c;clipboard\aновое\n",
	})
	plain := stripAnsi(card)
	if strings.Contains(card, "\x1b]") || strings.Contains(plain, "hijacked-title") || strings.Contains(plain, "tool-title") {
		t.Fatalf("inline diff card retained executable controls: %q", card)
	}
	if !strings.Contains(plain, "␛]52;c;clipboard␇новое") {
		t.Fatalf("inline diff card hid proposed control data:\n%s", plain)
	}
}

func TestMultiDiffPreviewEmptyStateClearsStaleContentAndActions(t *testing.T) {
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(90, 24)
	m.SetFiles([]DiffFile{{FilePath: "old.go", OldContent: "old", NewContent: "new"}})
	if !strings.Contains(stripAnsi(m.viewport.View()), "old") {
		t.Fatal("test setup did not populate the diff viewport")
	}

	m.SetFiles(nil)
	view := stripAnsi(m.View())
	for _, want := range []string{"Multi-File Diff Preview (0 files)", "No files to review", "Esc Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty multi-diff view missing %q:\n%s", want, view)
		}
	}
	for _, unavailable := range []string{"File 1/0", "Apply file", "Reject file", "Switch focus", "stale"} {
		if strings.Contains(view, unavailable) {
			t.Fatalf("empty multi-diff view contains unavailable or stale content %q:\n%s", unavailable, view)
		}
	}
	if m.currentIndex != -1 || m.viewport.YOffset != 0 {
		t.Fatalf("empty refresh left stale navigation: index=%d offset=%d", m.currentIndex, m.viewport.YOffset)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd != nil || updated.currentIndex != -1 {
		t.Fatal("y acted on an empty multi-diff")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc did not close an empty multi-diff")
	}
	response, ok := cmd().(MultiDiffPreviewResponseMsg)
	if !ok || len(response.Decisions) != 0 {
		t.Fatalf("empty close response = %#v", response)
	}
}

func TestMultiDiffPreviewLongListKeepsCurrentFileVisible(t *testing.T) {
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(100, 16)
	files := make([]DiffFile, 12)
	for i := range files {
		files[i] = DiffFile{FilePath: fmt.Sprintf("file-%02d.go", i), OldContent: "old", NewContent: "new"}
	}
	m.SetFiles(files)

	for range 8 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.currentIndex != 8 {
		t.Fatalf("current index=%d, want 8", m.currentIndex)
	}
	capacity := m.visibleFileCapacity()
	if m.currentIndex < m.listOffset || m.currentIndex >= m.listOffset+capacity {
		t.Fatalf("current file %d is outside list window [%d,%d)", m.currentIndex, m.listOffset, m.listOffset+capacity)
	}
	view := stripAnsi(m.View())
	if !strings.Contains(view, "file-08.go") || strings.Contains(view, "file-00.go") {
		t.Fatalf("rendered list did not follow current file window:\n%s", view)
	}
	if got := renderedLineCount(m.View()); got > 16 {
		t.Fatalf("wide multi-diff height=%d, want <=16:\n%s", got, view)
	}
}

func TestMultiDiffPreviewSplitListKeepsUnicodeFilenameIdentity(t *testing.T) {
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(minMultiDiffSplitWidth, 18)
	files := make([]DiffFile, 6)
	for i := range files {
		files[i] = DiffFile{
			FilePath:   fmt.Sprintf("deep/path/%02d-%s-important.go", i, strings.Repeat("🙂модуль", 8)),
			OldContent: "old",
			NewContent: "new",
		}
	}
	m.SetFiles(files)

	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"00-", "important.go"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("split list lost filename identity %q:\n%s", want, plain)
		}
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > minMultiDiffSplitWidth {
			t.Fatalf("split row %d width=%d: %q", row, got, stripAnsi(line))
		}
	}

	for range 4 {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	plain = stripAnsi(m.View())
	if !strings.Contains(plain, "04-") || !strings.Contains(plain, "important.go") {
		t.Fatalf("navigation lost current filename identity:\n%s", plain)
	}
}

func TestMultiDiffPreviewSinglePaneExplainsActiveFocus(t *testing.T) {
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(60, 18)
	m.SetFiles([]DiffFile{
		{FilePath: "one.go", OldContent: strings.Repeat("old\n", 30), NewContent: strings.Repeat("new\n", 30)},
		{FilePath: "two.go", OldContent: "old", NewContent: "new"},
	})

	view := stripAnsi(m.View())
	for _, want := range []string{"Navigate files", "Tab Focus diff"} {
		if !strings.Contains(view, want) {
			t.Fatalf("file focus footer missing %q:\n%s", want, view)
		}
	}
	initialIndex := m.currentIndex
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	view = stripAnsi(m.View())
	for _, want := range []string{"Scroll diff", "Tab Focus files"} {
		if !strings.Contains(view, want) {
			t.Fatalf("diff focus footer missing %q:\n%s", want, view)
		}
	}
	beforeOffset := m.viewport.YOffset
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.currentIndex != initialIndex || m.viewport.YOffset <= beforeOffset {
		t.Fatalf("diff focus routed Down incorrectly: index=%d offset=%d→%d", m.currentIndex, beforeOffset, m.viewport.YOffset)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.currentIndex != initialIndex+1 {
		t.Fatalf("file focus did not route Down to next file: index=%d", m.currentIndex)
	}

	m.SetSize(30, 18)
	if compact := stripAnsi(m.View()); !strings.Contains(compact, "↑/↓ Files") || !strings.Contains(compact, "Tab Diff") {
		t.Fatalf("compact file focus is ambiguous:\n%s", compact)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if compact := stripAnsi(m.View()); !strings.Contains(compact, "j/k Scroll") || !strings.Contains(compact, "Tab Files") {
		t.Fatalf("compact diff focus is ambiguous:\n%s", compact)
	}
}

func TestMultiDiffPreviewNarrowLayoutFitsTerminal(t *testing.T) {
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(30, 24)
	m.SetFiles([]DiffFile{
		{FilePath: "a/very/long/path/to/first-file.go", OldContent: "old", NewContent: "new"},
		{FilePath: "second.go", OldContent: "before", NewContent: "after"},
	})

	if m.useMultiDiffSplitLayout() {
		t.Fatal("30-column terminal should use the compact single-pane layout")
	}
	view := m.View()
	plain := stripAnsi(view)
	if strings.Contains(plain, "Files 1–") {
		t.Fatalf("narrow layout still rendered the side file pane:\n%s", plain)
	}
	for i, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 30 {
			t.Fatalf("narrow row %d width=%d, want <=30:\n%s", i, width, plain)
		}
	}
	if got := renderedLineCount(view); got > 24 {
		t.Fatalf("narrow multi-diff height=%d, want <=24:\n%s", got, plain)
	}
}

func TestDiffPreviewIgnoreWhitespaceHasVisibleAndHonestEffect(t *testing.T) {
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetSize(120, 24)
	m.SetContent("main.go", "func main() {\n\treturn\n}", "func main() {\n    return   \n}", "edit", false)
	if !m.effectiveSideBySide() || !m.hasVisibleChanges() {
		t.Fatal("test setup should begin with visible split-view changes")
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	if m.effectiveSideBySide() {
		t.Fatal("ignore-whitespace should use the filterable unified representation")
	}
	if m.hasVisibleChanges() {
		t.Fatalf("whitespace-only edit remained visible after enabling the filter:\n%s", m.diff)
	}
	view := stripAnsi(m.View())
	for _, want := range []string{
		"All changes are whitespace-only and currently hidden.",
		"Press I to show them.",
		"split→unified (ignore-ws)",
		"y Continue",
		"n Cancel",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("filtered diff view missing %q:\n%s", want, view)
		}
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	if !m.effectiveSideBySide() || !m.hasVisibleChanges() {
		t.Fatal("disabling ignore-whitespace did not restore the preferred split view and changes")
	}
}

func TestDiffPreviewNoChangeAndMissingMetadataRemainUnderstandable(t *testing.T) {
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetSize(80, 24)
	m.SetContent("", "same", "same", "", false)

	view := stripAnsi(m.View())
	for _, want := range []string{"Modified: (unknown file)", "No content changes detected.", "y Continue", "n Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no-change diff view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "→ Modified") {
		t.Fatalf("missing tool name left an orphan separator:\n%s", view)
	}
}

func TestDiffPreviewIgnoreWhitespaceKeepsRealChanges(t *testing.T) {
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetSize(100, 24)
	m.SetContent("main.go", "value := 1", "value   := 2", "edit", false)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})

	if !m.hasVisibleChanges() {
		t.Fatal("ignore-whitespace hid a real value change")
	}
	if strings.Contains(stripAnsi(m.View()), "All changes are whitespace-only") {
		t.Fatal("real change was mislabeled as whitespace-only")
	}
}

func TestDiffPreviewTinyLayoutFitsAndKeepsDecisionsVisible(t *testing.T) {
	m := NewDiffPreviewModel(DefaultStyles())
	m.SetSize(12, 14)
	m.SetContent("a/very/long/path/to/main.go", "before", "after", "replace", false)

	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"y Yes", "n No", "A All", "R No"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("tiny diff hid decision %q:\n%s", want, plain)
		}
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 12 {
			t.Fatalf("tiny diff row %d width=%d, want <=12: %q", row, got, stripAnsi(line))
		}
	}
	if got := renderedLineCount(view); got > 14 {
		t.Fatalf("tiny diff height=%d, want <=14:\n%s", got, plain)
	}
}

func TestMultiDiffPreviewTinyLayoutAndSnapshots(t *testing.T) {
	files := []DiffFile{{FilePath: "main.go", OldContent: "before", NewContent: "after"}}
	m := NewMultiDiffPreviewModel(DefaultStyles())
	m.SetSize(12, 14)
	m.SetFiles(files)
	if files[0].Diff != "" {
		t.Fatalf("SetFiles mutated caller-owned file with generated diff: %q", files[0].Diff)
	}

	callbackMutated := false
	m.SetCompleteCallback(func(decisions map[string]DiffDecision) {
		decisions["main.go"] = DiffReject
		decisions["injected.go"] = DiffApply
		callbackMutated = true
	})
	view := m.View()
	plain := stripAnsi(view)
	for _, want := range []string{"y Yes", "n No", "A All", "R No", "Enter Done…", "Esc Reject"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("tiny multi-diff hid action %q:\n%s", want, plain)
		}
	}
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > 12 {
			t.Fatalf("tiny multi-diff row %d width=%d, want <=12: %q", row, got, stripAnsi(line))
		}
	}
	if got := renderedLineCount(view); got > 14 {
		t.Fatalf("tiny multi-diff height=%d, want <=14:\n%s", got, plain)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil || !callbackMutated {
		t.Fatal("final per-file decision did not complete review")
	}
	response := cmd().(MultiDiffPreviewResponseMsg)
	if response.Decisions["main.go"] != DiffApply || len(response.Decisions) != 1 {
		t.Fatalf("callback mutation leaked into response: %+v", response.Decisions)
	}
}
