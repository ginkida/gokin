package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParentManagedSearchViewerHidesAndBlocksUnlinkedActions(t *testing.T) {
	m := NewModel()
	m.width, m.height = 90, 24
	m.state = StateSearchResults
	m.searchResults.SetSize(m.width, m.height)
	m.searchResults.SetResults("model", "grep", []SearchResult{{FilePath: "main.go", LineNumber: 12}})

	view := stripAnsi(m.searchResults.View())
	if !strings.Contains(view, "Open/Edit unavailable") {
		t.Fatalf("unlinked search panel should explain viewer mode:\n%s", view)
	}
	for _, unavailable := range []string{"Enter Open", "e Edit"} {
		if strings.Contains(view, unavailable) {
			t.Fatalf("unlinked search panel advertised %q:\n%s", unavailable, view)
		}
	}
	if _, cmd := m.searchResults.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("unlinked Enter emitted an Open action")
	}
	assertShortcutHints(t, m,
		[]string{"Space Preview", "y Copy path", "esc/q Close"},
		[]string{"Enter Open", "↑↓ Navigate", "PgUp/PgDn Page", "Ctrl+j/k Scroll preview"},
	)

	m.SetSearchResultActionCallback(func(SearchAction, string, int) {})
	view = stripAnsi(m.searchResults.View())
	for _, want := range []string{"Enter Open", "e Edit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("linked search panel missing %q:\n%s", want, view)
		}
	}
	if _, cmd := m.searchResults.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("linked Enter did not emit an Open action")
	}
	assertShortcutHints(t, m, []string{"Enter Open"}, nil)
}

func TestWorkspaceNavigationHintsFollowCardinalityAndOverflow(t *testing.T) {
	search := NewModel()
	search.width, search.height = 90, 24
	search.state = StateSearchResults
	search.searchResults.SetSize(search.width, search.height)
	search.searchResults.SetResults("model", "grep", []SearchResult{{FilePath: "main.go"}, {FilePath: "app.go"}})
	assertShortcutHints(t, search,
		[]string{"↑↓ Navigate"},
		[]string{"PgUp/PgDn Page"},
	)

	results := make([]SearchResult, 20)
	for i := range results {
		results[i] = SearchResult{FilePath: strings.Repeat("nested/", i%3) + "file.go"}
	}
	search.height = 14
	search.searchResults.SetSize(search.width, search.height)
	search.searchResults.SetResults("model", "grep", results)
	assertShortcutHints(t, search,
		[]string{"↑↓ Navigate", "PgUp/PgDn Page"},
		nil,
	)

	git := NewModel()
	git.width, git.height = 90, 14
	git.state = StateGitStatus
	git.gitStatusModel.SetSize(git.width, git.height)
	entries := make([]GitFileEntry, 12)
	for i := range entries {
		entries[i] = GitFileEntry{FilePath: strings.Repeat("nested/", i%3) + "file.go", Status: GitFileModified}
	}
	git.gitStatusModel.SetStatus(entries, "main", "", "")
	assertShortcutHints(t, git,
		[]string{"↑↓ Inspect", "PgUp/PgDn Page", "esc/q Close"},
		nil,
	)
	view := stripAnsi(git.gitStatusModel.View())
	for _, want := range []string{"↑/↓ Navigate", "PgUp/PgDn Page", "Home/End Jump"} {
		if !strings.Contains(view, want) {
			t.Fatalf("overflowing read-only Git list missing %q:\n%s", want, view)
		}
	}
}

func TestParentManagedGitViewerBlocksUnlinkedMutations(t *testing.T) {
	m := NewModel()
	m.width, m.height = 90, 24
	m.state = StateGitStatus
	m.gitStatusModel.SetSize(m.width, m.height)
	m.gitStatusModel.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")

	view := stripAnsi(m.gitStatusModel.View())
	if !strings.Contains(view, "Read-only · Git actions unavailable") {
		t.Fatalf("unlinked Git panel should explain read-only mode:\n%s", view)
	}
	for _, unavailable := range []string{"Stage/Unstage", "Show diff", "Reset", "Commit"} {
		if strings.Contains(view, unavailable) {
			t.Fatalf("unlinked Git panel advertised %q:\n%s", unavailable, view)
		}
	}

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeySpace},
		{Type: tea.KeyRunes, Runes: []rune{'d'}},
		{Type: tea.KeyRunes, Runes: []rune{'r'}},
		{Type: tea.KeyTab},
	} {
		updated, cmd := m.gitStatusModel.Update(key)
		if cmd != nil || updated.showDiff || updated.confirmReset || len(updated.selectedIndices) > 0 {
			t.Fatalf("unlinked key %q mutated viewer state: cmd=%v diff=%v reset=%v selected=%v", key.String(), cmd, updated.showDiff, updated.confirmReset, updated.selectedIndices)
		}
	}
	assertShortcutHints(t, m,
		[]string{"esc/q Close"},
		[]string{"↑↓ Inspect", "PgUp/PgDn Page", "Space Stage/unstage", "d Show diff"},
	)

	m.SetGitStatusActionCallback(func(GitAction, []string, string) {})
	view = stripAnsi(m.gitStatusModel.View())
	for _, want := range []string{"Space Stage/Unstage", "d Show diff", "r Reset"} {
		if !strings.Contains(view, want) {
			t.Fatalf("linked Git panel missing %q:\n%s", want, view)
		}
	}
	if _, cmd := m.gitStatusModel.Update(tea.KeyMsg{Type: tea.KeySpace}); cmd == nil {
		t.Fatal("linked stage action did not emit a message")
	}
	assertShortcutHints(t, m,
		[]string{"Space Stage/unstage", "d Show diff"},
		[]string{"↑↓ Navigate", "PgUp/PgDn Page"},
	)
}

func TestFileBrowserStatusHintsFollowEditingAndSelectionState(t *testing.T) {
	m := NewModel()
	m.state = StateFileBrowser
	assertShortcutHints(t, m,
		[]string{"/ Filter", "r Refresh", "esc/q Close"},
		[]string{"Enter Open/add", "y Add selection"},
	)

	m.fileBrowser.filter = "missing"
	assertShortcutHints(t, m, []string{"c Clear"}, nil)

	m.fileBrowser.filterActive = true
	m.fileBrowser.filterInput = m.fileBrowser.filter
	assertShortcutHints(t, m,
		[]string{"Type Filter", "Backspace Delete", "Enter/Esc Done"},
		[]string{"esc/q Close", "r Refresh"},
	)

	m.fileBrowser.filterActive = false
	m.fileBrowser.entries = []FileEntry{{Name: "main.go", Path: "main.go"}}
	m.fileBrowser.selectedFiles["main.go"] = true
	assertShortcutHints(t, m,
		[]string{"Enter Add to draft", "Space Select", "y Add selection", "esc/q Close"},
		[]string{"↑↓ Move", "PgUp/PgDn Page", "/ Filter", "r Refresh"},
	)
}
