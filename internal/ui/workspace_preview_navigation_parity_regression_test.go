package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestShortWorkspacePreviewWheelNavigatesTheResultList(t *testing.T) {
	wheel := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}

	t.Run("search", func(t *testing.T) {
		m := NewSearchResultsModel(DefaultStyles())
		m.SetSize(80, 14)
		results := make([]SearchResult, 10)
		for i := range results {
			results[i] = SearchResult{
				FilePath: fmt.Sprintf("file-%02d.go", i),
				Context:  []string{"short preview"},
			}
		}
		m.SetResults("file", "grep", results)
		m.showPreview = true
		m.SetSize(80, 14)
		m.updatePreview()
		if m.previewPane.TotalLineCount() > m.previewPane.Height {
			t.Fatalf("setup: search preview unexpectedly scrolls: lines=%d height=%d", m.previewPane.TotalLineCount(), m.previewPane.Height)
		}

		updated, _ := m.Update(wheel)
		want := min(m.viewport.MouseWheelDelta, len(results)-1)
		if updated.selectedIndex != want {
			t.Fatalf("wheel over short search preview selected=%d, want %d", updated.selectedIndex, want)
		}
		if preview := stripAnsi(updated.previewPane.View()); !strings.Contains(preview, fmt.Sprintf("file-%02d.go", want)) {
			t.Fatalf("wheel navigation did not refresh search preview for selection %d:\n%s", want, preview)
		}
		assertSelectionInViewport(t, updated.selectedIndex, updated.viewport.YOffset, updated.viewport.Height)
	})

	t.Run("git", func(t *testing.T) {
		m := NewGitStatusModel(DefaultStyles())
		m.SetSize(80, 16)
		entries := make([]GitFileEntry, 10)
		for i := range entries {
			entries[i] = GitFileEntry{FilePath: fmt.Sprintf("file-%02d.go", i), Status: GitFileModified}
		}
		m.SetStatus(entries, "main", "", "")
		m.showDiff = true
		m.SetDiff("short diff")
		m.SetSize(80, 16)
		if m.diffViewport.TotalLineCount() > m.diffViewport.Height {
			t.Fatalf("setup: Git diff unexpectedly scrolls: lines=%d height=%d", m.diffViewport.TotalLineCount(), m.diffViewport.Height)
		}

		updated, cmd := m.Update(wheel)
		want := min(m.viewport.MouseWheelDelta, len(entries)-1)
		if got := updated.selectedPosition(); got != want {
			t.Fatalf("wheel over short Git diff selected position=%d, want %d", got, want)
		}
		if cmd == nil {
			t.Fatal("wheel navigation did not request the newly selected file diff")
		}
		msg, ok := cmd().(GitStatusActionMsg)
		if !ok || msg.Action != GitActionDiff || len(msg.Files) != 1 || msg.Files[0] != entries[want].FilePath {
			t.Fatalf("wheel navigation emitted %#v, want diff request for %q", msg, entries[want].FilePath)
		}
		assertSelectionInViewport(t, updated.entryRows[updated.selectedIndex], updated.viewport.YOffset, updated.viewport.Height)
	})
}

func TestGitDiffKeyboardScrollHasTruthfulHints(t *testing.T) {
	m := NewGitStatusModel(DefaultStyles())
	m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
	m.showDiff = true
	m.SetDiff(strings.Repeat("diff line\n", 30))
	m.SetSize(80, 16)

	if view := stripAnsi(m.View()); !strings.Contains(view, "Ctrl+j/k Scroll diff") {
		t.Fatalf("scrollable Git diff hides its keyboard scroll control:\n%s", view)
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if updated.diffViewport.YOffset == 0 {
		t.Fatal("Ctrl+j did not scroll the visible Git diff")
	}
	back, _ := updated.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if back.diffViewport.YOffset >= updated.diffViewport.YOffset {
		t.Fatalf("Ctrl+k did not scroll Git diff back: before=%d after=%d", updated.diffViewport.YOffset, back.diffViewport.YOffset)
	}

	parent := NewModel()
	parent.width, parent.height = 80, 16
	parent.state = StateGitStatus
	parent.gitStatusModel = m
	if hints := plainShortcutHints(parent.contextualShortcutHintPairs()); !strings.Contains(hints, "Ctrl+j/k Scroll diff") {
		t.Fatalf("status hints omit scrollable Git diff keyboard control: %q", hints)
	}

	m.SetDiff("short diff")
	if view := stripAnsi(m.View()); strings.Contains(view, "Ctrl+j/k Scroll diff") {
		t.Fatalf("short Git diff advertises dead scrolling:\n%s", view)
	}
}
