package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSingleDiffInspectionHintsFollowOverflowAndAvailability(t *testing.T) {
	short := NewDiffPreviewModel(DefaultStyles())
	short.SetSize(80, 24)
	short.SetContent("main.go", "old\n", "new\n", "write", false)
	host := NewModel()
	host.state = StateDiffPreview
	host.diffPreview = short
	assertShortcutHints(t, host,
		[]string{"y Apply", "n Reject", "[ ] Changes"},
		[]string{"j/k Scroll"},
	)
	shortView := stripAnsi(short.View())
	for _, dead := range []string{"j/k Scroll", "g/G Top/Bottom", "Ctrl+D/U Half page"} {
		if strings.Contains(shortView, dead) {
			t.Fatalf("short diff advertised dead action %q:\n%s", dead, shortView)
		}
	}

	long := NewDiffPreviewModel(DefaultStyles())
	long.SetSize(72, 14)
	long.SetContent("main.go", strings.Repeat("old line\n", 40), strings.Repeat("new line\n", 40), "write", false)
	long.MarkResponseUnavailable()
	if !long.canScrollDiff() {
		t.Fatal("test fixture did not create an overflowing diff")
	}
	host.diffPreview = long
	assertShortcutHints(t, host,
		[]string{"esc Cancel", "j/k Scroll", "[ ] Changes"},
		[]string{"y Apply", "A Apply all"},
	)
	longView := stripAnsi(long.View())
	for _, want := range []string{"Unavailable: cannot submit diff decision", "Esc Cancel", "j/k Scroll", "view controls remain available"} {
		if !strings.Contains(longView, want) {
			t.Fatalf("unavailable long diff missing %q:\n%s", want, longView)
		}
	}
	before := long.viewport.YOffset
	long, _ = long.Update(tea.KeyMsg{Type: tea.KeyDown})
	if long.viewport.YOffset <= before {
		t.Fatalf("advertised unavailable-view scrolling did not move: %d→%d", before, long.viewport.YOffset)
	}
}

func TestMultiDiffStatusHintsFollowFocusAvailabilityAndConfirmation(t *testing.T) {
	multi := NewMultiDiffPreviewModel(DefaultStyles())
	multi.SetSize(60, 16)
	multi.SetFiles([]DiffFile{
		{FilePath: "one.go", OldContent: strings.Repeat("old\n", 30), NewContent: strings.Repeat("new\n", 30)},
		{FilePath: "two.go", OldContent: "old", NewContent: "new"},
	})
	host := NewModel()
	host.state = StateMultiDiffPreview
	host.multiDiffPreview = multi
	assertShortcutHints(t, host,
		[]string{"y/n Decide file", "A/R Decide rest", "Enter Finish…", "↑↓ Files", "Tab Focus diff"},
		nil,
	)

	multi.MarkResponseUnavailable()
	host.multiDiffPreview = multi
	assertShortcutHints(t, host,
		[]string{"esc Cancel", "↑↓ Files", "Tab Focus diff"},
		[]string{"y/n Decide file", "A/R Decide rest", "Enter Finish…"},
	)
	view := stripAnsi(multi.View())
	for _, want := range []string{"view controls remain available", "Navigate files", "Focus diff"} {
		if !strings.Contains(view, want) {
			t.Fatalf("unavailable multi diff missing %q:\n%s", want, view)
		}
	}
	moved, _ := multi.Update(tea.KeyMsg{Type: tea.KeyDown})
	if moved.currentIndex != 1 {
		t.Fatalf("advertised file navigation did not move: index=%d", moved.currentIndex)
	}

	confirm := NewMultiDiffPreviewModel(DefaultStyles())
	confirm.SetSize(60, 16)
	confirm.SetFiles([]DiffFile{{FilePath: "a.go", OldContent: "old", NewContent: "new"}, {FilePath: "b.go", OldContent: "old", NewContent: "new"}})
	confirm, cmd := confirm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !confirm.confirmFinish {
		t.Fatal("test fixture did not enter finish confirmation")
	}
	host.multiDiffPreview = confirm
	assertShortcutHints(t, host,
		[]string{"esc/q Back to review", "Enter Confirm"},
		[]string{"y/n Decide file", "A/R Decide rest", "Tab Focus diff", "↑↓ Files"},
	)
}

func TestSingleShortMultiDiffDoesNotAdvertiseDeadFocusSwitch(t *testing.T) {
	multi := NewMultiDiffPreviewModel(DefaultStyles())
	multi.SetSize(80, 24)
	multi.SetFiles([]DiffFile{{FilePath: "only.go", OldContent: "old", NewContent: "new"}})
	host := NewModel()
	host.state = StateMultiDiffPreview
	host.multiDiffPreview = multi
	assertShortcutHints(t, host,
		[]string{"y/n Decide file", "A/R Decide rest", "Enter Finish…"},
		[]string{"↑↓ Files", "Tab Focus diff", "j/k Scroll diff"},
	)
	view := stripAnsi(multi.View())
	for _, dead := range []string{"Navigate files", "Focus diff", "Scroll diff"} {
		if strings.Contains(view, dead) {
			t.Fatalf("single short multi diff advertised dead action %q:\n%s", dead, view)
		}
	}
}
