package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTinyMultiDiffConfirmShowsConsequenceAndConfirmation(t *testing.T) {
	for _, height := range []int{4, 5} {
		t.Run(itoa(height), func(t *testing.T) {
			m := NewModel()
			m.state = StateMultiDiffPreview
			m.multiDiffPreview.SetFiles([]DiffFile{
				{FilePath: "kept.go", OldContent: "old", NewContent: "new"},
				{FilePath: "pending.go", OldContent: "old", NewContent: "new"},
			})
			m.multiDiffPreview.decisions[0] = DiffApply
			m.multiDiffPreview.currentIndex = 1
			m.multiDiffPreview.confirmFinish = true
			m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: height})

			view := m.View()
			plain := stripAnsi(view)
			for _, want := range []string{"Reject", "Enter Confirm", "Esc"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("20x%d confirm frame hid %q:\n%s", height, want, plain)
				}
			}
			if got := lipgloss.Height(view); got != height {
				t.Fatalf("20x%d confirm frame rendered %d rows:\n%s", height, got, plain)
			}
			for row, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 20 {
					t.Fatalf("20x%d row %d overflow=%d: %q", height, row, got, stripAnsi(line))
				}
			}
		})
	}
}

func TestUnreadableTinyDiffBlocksUnsafeApplyAndConfirmation(t *testing.T) {
	for width := 1; width <= 7; width++ {
		t.Run(itoa(width), func(t *testing.T) {
			wantResize := "↔"
			if width >= 6 {
				wantResize = "Resize"
			}

			single := NewDiffPreviewModel(DefaultStyles())
			single.SetContent("main.go", "old", "new", "edit", false)
			single.SetSize(width, 6)
			for _, key := range []tea.KeyMsg{
				{Type: tea.KeyRunes, Runes: []rune{'y'}},
				{Type: tea.KeyRunes, Runes: []rune{'A'}},
			} {
				var cmd tea.Cmd
				single, cmd = single.Update(key)
				if cmd != nil || single.decision != DiffPending || single.responsePending {
					t.Fatalf("width=%d single %q escaped resize gate: decision=%v pending=%v cmd=%v", width, key.String(), single.decision, single.responsePending, cmd != nil)
				}
			}
			singleView := stripAnsi(single.View())
			if !strings.Contains(singleView, wantResize) || strings.Contains(singleView, "Apply") {
				t.Fatalf("width=%d single resize affordance=%q, want %q and no Apply", width, singleView, wantResize)
			}
			rejectedSingle, rejectCmd := single.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
			if rejectCmd == nil || rejectedSingle.decision != DiffReject {
				t.Fatalf("width=%d single safe reject unavailable: decision=%v cmd=%v", width, rejectedSingle.decision, rejectCmd != nil)
			}

			multi := NewMultiDiffPreviewModel(DefaultStyles())
			multi.SetFiles([]DiffFile{{FilePath: "main.go", OldContent: "old", NewContent: "new"}})
			multi.SetSize(width, 6)
			for _, key := range []tea.KeyMsg{
				{Type: tea.KeyRunes, Runes: []rune{'y'}},
				{Type: tea.KeyRunes, Runes: []rune{'A'}},
				{Type: tea.KeyEnter},
			} {
				var cmd tea.Cmd
				multi, cmd = multi.Update(key)
				if cmd != nil || multi.decisions[0] != DiffPending || multi.confirmFinish || multi.responsePending {
					t.Fatalf("width=%d multi %q escaped resize gate: decision=%v confirm=%v pending=%v cmd=%v", width, key.String(), multi.decisions[0], multi.confirmFinish, multi.responsePending, cmd != nil)
				}
			}
			multiView := stripAnsi(multi.View())
			if !strings.Contains(multiView, wantResize) || strings.Contains(multiView, "Apply") || strings.Contains(multiView, "Enter") {
				t.Fatalf("width=%d multi resize affordance=%q, want %q and no unsafe actions", width, multiView, wantResize)
			}
			rejectedMulti, rejectCmd := multi.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
			if rejectCmd == nil || rejectedMulti.decisions[0] != DiffReject {
				t.Fatalf("width=%d multi safe reject unavailable: decision=%v cmd=%v", width, rejectedMulti.decisions[0], rejectCmd != nil)
			}

			confirm := NewMultiDiffPreviewModel(DefaultStyles())
			confirm.SetFiles([]DiffFile{{FilePath: "main.go", OldContent: "old", NewContent: "new"}})
			confirm.SetSize(width, 6)
			confirm.confirmFinish = true
			blocked, confirmCmd := confirm.Update(tea.KeyMsg{Type: tea.KeyEnter})
			if confirmCmd != nil || !blocked.confirmFinish || blocked.decisions[0] != DiffPending {
				t.Fatalf("width=%d invisible confirmation was accepted: confirm=%v decision=%v cmd=%v", width, blocked.confirmFinish, blocked.decisions[0], confirmCmd != nil)
			}
			confirmView := stripAnsi(blocked.View())
			if !strings.Contains(confirmView, wantResize) || strings.Contains(confirmView, "Enter") {
				t.Fatalf("width=%d confirm resize affordance=%q, want %q and no Enter", width, confirmView, wantResize)
			}
			back, backCmd := blocked.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			if backCmd != nil || back.confirmFinish || back.decisions[0] != DiffPending {
				t.Fatalf("width=%d safe confirm back unavailable: confirm=%v decision=%v cmd=%v", width, back.confirmFinish, back.decisions[0], backCmd != nil)
			}
		})
	}
}
