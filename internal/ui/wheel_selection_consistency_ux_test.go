package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestListWheelMovesLogicalSelectionByViewportDelta(t *testing.T) {
	t.Run("search results", func(t *testing.T) {
		m := NewSearchResultsModel(DefaultStyles())
		m.SetSize(80, 16)
		results := make([]SearchResult, 10)
		for i := range results {
			results[i] = SearchResult{FilePath: fmt.Sprintf("file-%02d.go", i), LineNumber: i + 1}
		}
		m.SetResults("query", "grep", results)

		m, _ = m.Update(wheelDown())
		if m.selectedIndex != m.viewport.MouseWheelDelta {
			t.Fatalf("search selection=%d, want %d", m.selectedIndex, m.viewport.MouseWheelDelta)
		}
		assertSelectionInViewport(t, m.selectedIndex, m.viewport.YOffset, m.viewport.Height)
	})

	t.Run("git status", func(t *testing.T) {
		m := NewGitStatusModel(DefaultStyles())
		m.SetSize(80, 18)
		entries := make([]GitFileEntry, 10)
		for i := range entries {
			entries[i] = GitFileEntry{FilePath: fmt.Sprintf("file-%02d.go", i), Status: GitFileModified}
		}
		m.SetStatus(entries, "main", "", "")

		m, _ = m.Update(wheelDown())
		if got := m.selectedPosition(); got != m.viewport.MouseWheelDelta {
			t.Fatalf("git selection=%d, want %d", got, m.viewport.MouseWheelDelta)
		}
		row := m.entryRows[m.selectedIndex]
		assertSelectionInViewport(t, row, m.viewport.YOffset, m.viewport.Height)
	})

	t.Run("file browser", func(t *testing.T) {
		m := NewFileBrowserModel(DefaultStyles())
		m.entries = make([]FileEntry, 10)
		for i := range m.entries {
			m.entries[i] = FileEntry{Name: fmt.Sprintf("file-%02d.go", i), Path: fmt.Sprintf("/tmp/file-%02d.go", i)}
		}
		m.SetSize(80, 18)

		m, _ = m.Update(wheelDown())
		if m.selectedIndex != m.viewport.MouseWheelDelta {
			t.Fatalf("file selection=%d, want %d", m.selectedIndex, m.viewport.MouseWheelDelta)
		}
		assertSelectionInViewport(t, m.selectedIndex, m.viewport.YOffset, m.viewport.Height)
	})

	t.Run("multi diff files", func(t *testing.T) {
		m := NewMultiDiffPreviewModel(DefaultStyles())
		m.SetSize(90, 22)
		files := make([]DiffFile, 10)
		for i := range files {
			files[i] = DiffFile{FilePath: fmt.Sprintf("file-%02d.go", i), OldContent: "old\n", NewContent: "new\n"}
		}
		m.SetFiles(files)

		m, _ = m.Update(wheelDown())
		if m.currentIndex != m.viewport.MouseWheelDelta {
			t.Fatalf("multi-diff selection=%d, want %d", m.currentIndex, m.viewport.MouseWheelDelta)
		}
		if m.currentIndex < m.listOffset || m.currentIndex >= m.listOffset+m.visibleFileCapacity() {
			t.Fatalf("multi-diff selection %d is outside visible list [%d,%d)", m.currentIndex, m.listOffset, m.listOffset+m.visibleFileCapacity())
		}
	})
}

func TestWheelScrollsPreviewWithoutChangingListSelection(t *testing.T) {
	t.Run("search preview", func(t *testing.T) {
		m := NewSearchResultsModel(DefaultStyles())
		m.SetSize(80, 14)
		m.SetResults("query", "grep", []SearchResult{{
			FilePath: "main.go", LineNumber: 20, Context: strings.Split(strings.Repeat("context line\n", 30), "\n"),
		}})
		m.showPreview = true
		m.SetSize(80, 14)
		m.updatePreview()

		m, _ = m.Update(wheelDown())
		if m.selectedIndex != 0 || m.previewPane.YOffset == 0 {
			t.Fatalf("preview wheel changed selection or did not scroll: selected=%d offset=%d", m.selectedIndex, m.previewPane.YOffset)
		}
	})

	t.Run("git diff", func(t *testing.T) {
		m := NewGitStatusModel(DefaultStyles())
		m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}, {FilePath: "other.go", Status: GitFileModified}}, "main", "", "")
		m.showDiff = true
		m.SetSize(80, 16)
		m.SetDiff(strings.Repeat("diff line\n", 30))

		m, _ = m.Update(wheelDown())
		if m.selectedIndex != 0 || m.diffViewport.YOffset == 0 {
			t.Fatalf("diff wheel changed selection or did not scroll: selected=%d offset=%d", m.selectedIndex, m.diffViewport.YOffset)
		}
	})
}

func TestWheelReleaseAndShiftDoNotMoveListSelection(t *testing.T) {
	m := NewSearchResultsModel(DefaultStyles())
	m.SetResults("query", "grep", []SearchResult{{FilePath: "one"}, {FilePath: "two"}})

	m, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonWheelDown})
	m, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown, Shift: true})
	if m.selectedIndex != 0 {
		t.Fatalf("release/shift wheel moved selection to %d", m.selectedIndex)
	}
}

func assertSelectionInViewport(t *testing.T, row, offset, height int) {
	t.Helper()
	if row < offset || row >= offset+max(height, 1) {
		t.Fatalf("selected row %d is outside viewport [%d,%d)", row, offset, offset+max(height, 1))
	}
}
