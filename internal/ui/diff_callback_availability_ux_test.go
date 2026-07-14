package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUnavailableSingleDiffApplyStaysOpenUntilExplicitCancel(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 18})
	req := DiffPreviewRequestMsg{FilePath: "main.go", OldContent: "old\n", NewContent: "new\n", ToolName: "write"}
	m.diffRequest = &req
	m.diffPreview.SetSize(m.width, m.height)
	m.diffPreview.SetContent(req.FilePath, req.OldContent, req.NewContent, req.ToolName, false)
	m.state = StateDiffPreview
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	updatedAny, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("apply key did not produce a decision message")
	}
	updated := updatedAny.(Model)
	updatedAny, _ = updated.Update(cmd())
	blocked := updatedAny.(Model)
	if blocked.state != StateDiffPreview || blocked.diffRequest == nil || !blocked.diffPreview.responseUnavailable || cancelled != 0 {
		t.Fatalf("unavailable apply escaped review: state=%v request=%v unavailable=%v cancelled=%d", blocked.state, blocked.diffRequest, blocked.diffPreview.responseUnavailable, cancelled)
	}
	view := stripAnsi(blocked.View())
	for _, want := range []string{"Unavailable: cannot submit diff decision", "Esc Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("blocked diff review missing %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"y Apply", "A Accept all"} {
		if strings.Contains(view, stale) {
			t.Fatalf("blocked diff review advertises unavailable action %q:\n%s", stale, view)
		}
	}

	updatedAny, cmd = blocked.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc did not produce a reject/cancel decision")
	}
	updated = updatedAny.(Model)
	updatedAny, _ = updated.Update(cmd())
	closed := updatedAny.(Model)
	if closed.state != StateInput || closed.diffRequest != nil || cancelled != 1 {
		t.Fatalf("Esc did not cancel unavailable diff: state=%v request=%v cancelled=%d", closed.state, closed.diffRequest, cancelled)
	}
}

func TestUnavailableMultiDiffApplyStaysOpenUntilExplicitCancel(t *testing.T) {
	m := NewModel()
	m.applyResize(&tea.WindowSizeMsg{Width: 72, Height: 18})
	files := []DiffFile{{FilePath: "a.go", OldContent: "old\n", NewContent: "new\n"}}
	req := MultiDiffPreviewRequestMsg{Files: files}
	m.multiDiffRequest = &req
	m.multiDiffPreview.SetSize(m.width, m.height)
	m.multiDiffPreview.SetFiles(files)
	m.state = StateMultiDiffPreview
	cancelled := 0
	m.SetCancelCallback(func() { cancelled++ })

	updatedAny, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("multi apply did not produce completion message")
	}
	updated := updatedAny.(Model)
	updatedAny, _ = updated.Update(cmd())
	blocked := updatedAny.(Model)
	if blocked.state != StateMultiDiffPreview || blocked.multiDiffRequest == nil || !blocked.multiDiffPreview.responseUnavailable || cancelled != 0 {
		t.Fatalf("unavailable multi apply escaped review: state=%v request=%v unavailable=%v cancelled=%d", blocked.state, blocked.multiDiffRequest, blocked.multiDiffPreview.responseUnavailable, cancelled)
	}
	view := stripAnsi(blocked.View())
	for _, want := range []string{"Unavailable: cannot submit diff decisions", "Esc Cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("blocked multi review missing %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"Apply file", "Apply remaining", "Enter Finish"} {
		if strings.Contains(view, stale) {
			t.Fatalf("blocked multi review advertises unavailable action %q:\n%s", stale, view)
		}
	}

	updatedAny, cmd = blocked.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc did not produce a reject-all completion message")
	}
	updated = updatedAny.(Model)
	updatedAny, _ = updated.Update(cmd())
	closed := updatedAny.(Model)
	if closed.state != StateInput || closed.multiDiffRequest != nil || cancelled != 1 {
		t.Fatalf("Esc did not cancel unavailable multi diff: state=%v request=%v cancelled=%d", closed.state, closed.multiDiffRequest, cancelled)
	}
}
