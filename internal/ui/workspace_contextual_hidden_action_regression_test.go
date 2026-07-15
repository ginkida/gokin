package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func requireContextHints(t *testing.T, m *Model, want, notWant []string) {
	t.Helper()
	got := plainShortcutHints(m.contextualShortcutHintPairs())
	for _, fragment := range want {
		if !strings.Contains(got, fragment) {
			t.Errorf("missing contextual hint %q in %q", fragment, got)
		}
	}
	for _, fragment := range notWant {
		if strings.Contains(got, fragment) {
			t.Errorf("hidden action %q remains in contextual hints %q", fragment, got)
		}
	}
}

func TestReviewContextHintsFollowTinyAndResponseState(t *testing.T) {
	t.Run("single diff", func(t *testing.T) {
		m := NewModel()
		m.state = StateDiffPreview
		m.diffPreview.SetContent("main.go", "old", "new", "edit", false)
		m.diffPreview.SetSize(7, 6)
		requireContextHints(t, m,
			[]string{"↔ Resize", "n/esc Reject"},
			[]string{"y Apply", "A Apply all"},
		)

		m.diffPreview.SetSize(8, 4)
		requireContextHints(t, m, []string{"y Apply", "A Apply all"}, []string{"↔ Resize"})

		m.diffPreview.responsePending = true
		requireContextHints(t, m, nil, []string{"y Apply", "n Reject", "A Apply all", "R Reject all"})
		m.diffPreview.responsePending = false
		m.diffPreview.responseUnavailable = true
		requireContextHints(t, m, []string{"esc Cancel"}, []string{"y Apply", "n Reject"})
	})

	t.Run("multi diff", func(t *testing.T) {
		m := NewModel()
		m.state = StateMultiDiffPreview
		m.multiDiffPreview.SetFiles([]DiffFile{{FilePath: "main.go", OldContent: "old", NewContent: "new"}})
		m.multiDiffPreview.SetSize(7, 6)
		requireContextHints(t, m,
			[]string{"↔ Resize", "n Reject file", "esc Reject all"},
			[]string{"y/n Decide file", "A/R Decide rest", "Enter Finish"},
		)

		m.multiDiffPreview.confirmFinish = true
		requireContextHints(t, m,
			[]string{"↔ Resize", "esc/q Back to review"},
			[]string{"Enter Confirm", "n Reject file"},
		)

		m.multiDiffPreview.confirmFinish = false
		m.multiDiffPreview.SetSize(8, 4)
		requireContextHints(t, m, []string{"y/n Decide file", "Enter Finish"}, []string{"↔ Resize"})

		m.multiDiffPreview.responsePending = true
		requireContextHints(t, m, nil, []string{"y/n Decide file", "A/R Decide rest", "Enter Finish", "Enter Confirm"})
		m.multiDiffPreview.responsePending = false
		m.multiDiffPreview.responseUnavailable = true
		requireContextHints(t, m, []string{"esc Cancel"}, []string{"y/n Decide file", "n Reject file"})
	})
}

func TestWorkspaceContextHintsFollowHiddenTargetBoundary(t *testing.T) {
	t.Run("search", func(t *testing.T) {
		m := NewModel()
		m.state = StateSearchResults
		m.searchResults.SetActionsLinked(true)
		m.searchResults.SetResults("needle", "grep", []SearchResult{{FilePath: "main.go", Content: "needle"}})
		m.searchResults.SetSize(minSearchTargetWidth-1, 10)
		requireContextHints(t, m,
			[]string{"↔ Resize", "esc/q Close"},
			[]string{"Enter Open", "y Copy path", "Space Preview"},
		)
		m.searchResults.SetSize(minSearchTargetWidth, 5)
		requireContextHints(t, m, []string{"Enter Open", "y Copy path"}, []string{"↔ Resize"})
	})

	t.Run("git", func(t *testing.T) {
		m := NewModel()
		m.state = StateGitStatus
		m.gitStatusModel.SetActionsLinked(true)
		m.gitStatusModel.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
		m.gitStatusModel.SetSize(minGitTargetWidth-1, 10)
		requireContextHints(t, m,
			[]string{"↔ Resize", "esc/q Close"},
			[]string{"Space Stage/unstage", "d Show diff"},
		)

		m.gitStatusModel.confirmReset = true
		m.gitStatusModel.pendingResetFiles = []string{"main.go"}
		requireContextHints(t, m,
			[]string{"↔ Resize", "esc/q Cancel"},
			[]string{"Enter Confirm reset"},
		)

		m.gitStatusModel.confirmReset = false
		m.gitStatusModel.SetSize(minGitTargetWidth, 5)
		requireContextHints(t, m, []string{"Space Stage/unstage", "d Show diff"}, []string{"↔ Resize"})
	})

	t.Run("file browser", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "main.go")
		if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := NewModel()
		m.state = StateFileBrowser
		if err := m.fileBrowser.SetPath(dir); err != nil {
			t.Fatal(err)
		}
		selectFileBrowserPath(t, &m.fileBrowser, file)
		m.fileBrowser.SetSize(minFileBrowserTargetWidth-1, 10)
		requireContextHints(t, m,
			[]string{"↔ Resize", "esc/q Close"},
			[]string{"Enter Add to draft", "Space Select"},
		)
		m.fileBrowser.SetSize(minFileBrowserTargetWidth, 5)
		requireContextHints(t, m, []string{"Enter Add to draft", "Space Select"}, []string{"↔ Resize"})
	})
}
