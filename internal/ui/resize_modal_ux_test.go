package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestResizeRoutesToActiveViewportModalAndCollapsesSplits(t *testing.T) {
	t.Run("diff preview", func(t *testing.T) {
		m := NewModel()
		m.state = StateDiffPreview
		m.diffPreview.SetSize(120, 30)
		m.diffPreview.SetContent("main.go", "old", "new", "edit", false)
		m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
		if m.diffPreview.width != 50 || m.diffPreview.height != 12 || m.diffPreview.effectiveSideBySide() {
			t.Fatalf("diff retained stale geometry: %dx%d split=%v", m.diffPreview.width, m.diffPreview.height, m.diffPreview.effectiveSideBySide())
		}
	})

	t.Run("multi diff", func(t *testing.T) {
		m := NewModel()
		m.state = StateMultiDiffPreview
		m.multiDiffPreview.SetSize(120, 30)
		m.multiDiffPreview.SetFiles([]DiffFile{{FilePath: "main.go", OldContent: "old", NewContent: "new"}})
		m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
		if m.multiDiffPreview.width != 50 || m.multiDiffPreview.height != 12 || m.multiDiffPreview.useMultiDiffSplitLayout() {
			t.Fatalf("multi-diff retained stale geometry: %dx%d split=%v", m.multiDiffPreview.width, m.multiDiffPreview.height, m.multiDiffPreview.useMultiDiffSplitLayout())
		}
	})

	t.Run("search results", func(t *testing.T) {
		m := NewModel()
		m.state = StateSearchResults
		m.searchResults.SetResults("needle", "grep", []SearchResult{{
			FilePath: "internal/ui/a_very_long_search_result_name.go",
			Content:  "needle",
			Context:  []string{"before", "needle", "after"},
		}})
		m.searchResults.showPreview = true
		m.searchResults.SetSize(120, 30)
		m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
		if m.searchResults.width != 50 || m.searchResults.height != 12 || m.searchResults.viewport.Width != 46 || m.searchResults.previewPane.Width != 46 {
			t.Fatalf("search retained stale geometry: model=%dx%d list=%d preview=%d", m.searchResults.width, m.searchResults.height, m.searchResults.viewport.Width, m.searchResults.previewPane.Width)
		}
		assertFrameGeometry(t, m, 50, 12)
	})

	t.Run("git status", func(t *testing.T) {
		m := NewModel()
		m.state = StateGitStatus
		m.gitStatusModel.SetStatus([]GitFileEntry{{FilePath: "internal/ui/a_very_long_changed_file.go", Status: GitFileModified}}, "main", "origin/main", "")
		m.gitStatusModel.showDiff = true
		m.gitStatusModel.SetDiff("-old\n+new")
		m.gitStatusModel.SetSize(120, 30)
		m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
		if m.gitStatusModel.width != 50 || m.gitStatusModel.height != 12 || m.gitStatusModel.viewport.Width != 46 || m.gitStatusModel.diffViewport.Width != 46 {
			t.Fatalf("git retained stale geometry: model=%dx%d list=%d diff=%d", m.gitStatusModel.width, m.gitStatusModel.height, m.gitStatusModel.viewport.Width, m.gitStatusModel.diffViewport.Width)
		}
		assertFrameGeometry(t, m, 50, 12)
	})

	t.Run("file browser", func(t *testing.T) {
		m := NewModel()
		m.state = StateFileBrowser
		m.fileBrowser.entries = []FileEntry{{Name: "long-file-name.go", Path: "/tmp/long-file-name.go"}}
		m.fileBrowser.previewEnabled = true
		m.fileBrowser.SetSize(120, 30)
		if m.fileBrowser.previewWidth == 0 {
			t.Fatal("wide file-browser setup did not enable split layout")
		}
		m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
		if m.fileBrowser.width != 50 || m.fileBrowser.height != 12 || m.fileBrowser.previewWidth != 0 || m.fileBrowser.listWidth != 46 {
			t.Fatalf("file browser retained stale geometry: model=%dx%d list=%d preview=%d", m.fileBrowser.width, m.fileBrowser.height, m.fileBrowser.listWidth, m.fileBrowser.previewWidth)
		}
		assertFrameGeometry(t, m, 50, 12)
	})

	t.Run("command palette", func(t *testing.T) {
		m := NewModel()
		m.state = StateCommandPalette
		m.commandPalette.SetSize(120, 30)
		m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
		if m.commandPalette.width != 50 || m.commandPalette.height != 12 {
			t.Fatalf("palette retained size %dx%d", m.commandPalette.width, m.commandPalette.height)
		}
	})
}

func assertFrameGeometry(t *testing.T, m *Model, width, height int) {
	t.Helper()
	view := m.View()
	if got := lipgloss.Height(view); got != height {
		t.Fatalf("resized frame height=%d, want %d", got, height)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("resized frame row %d width=%d, want <=%d:\n%s", i, got, width, stripAnsi(view))
		}
	}
}

func TestResizeReflowsLoadedDiffContentAcrossLayoutBreakpoint(t *testing.T) {
	m := NewModel()
	m.state = StateDiffPreview
	m.diffPreview.SetSize(120, 24)
	m.diffPreview.SetContent("main.go", "old line", "new line", "edit", false)
	if !strings.Contains(stripAnsi(m.diffPreview.viewport.View()), "OLD") {
		t.Fatal("wide diff setup should use split content")
	}

	m.applyResize(&tea.WindowSizeMsg{Width: 60, Height: 18})
	narrow := stripAnsi(m.diffPreview.viewport.View())
	if strings.Contains(narrow, "OLD") || !strings.Contains(narrow, "-old") || !strings.Contains(narrow, "+new") {
		t.Fatalf("resize did not regenerate unified diff content:\n%s", narrow)
	}
}

func TestResizeDoesNotTouchInactiveFileBrowser(t *testing.T) {
	m := NewModel()
	m.state = StateInput
	m.fileBrowser.SetSize(100, 24)
	m.fileBrowser.previewEnabled = true
	m.fileBrowser.entries = []FileEntry{{Name: "missing", Path: "/definitely/missing/file"}}

	m.applyResize(&tea.WindowSizeMsg{Width: 50, Height: 12})
	if m.fileBrowser.width != 100 || m.fileBrowser.height != 24 {
		t.Fatalf("inactive browser was resized/reloaded: %dx%d", m.fileBrowser.width, m.fileBrowser.height)
	}
}
