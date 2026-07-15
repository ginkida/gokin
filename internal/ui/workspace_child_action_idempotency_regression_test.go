package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkspaceChildTerminalCallbackFiresOnceBeforeCommandConsumption(t *testing.T) {
	t.Run("search", func(t *testing.T) {
		m := NewSearchResultsModel(DefaultStyles())
		m.SetResults("needle", "grep", []SearchResult{{FilePath: "main.go", Content: "needle"}})
		calls := 0
		m.SetActionCallback(func(SearchAction, string, int) { calls++ })
		first, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		second, secondCmd := first.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if firstCmd == nil || calls != 1 || secondCmd != nil || !second.actionPending {
			t.Fatalf("search callbacks=%d firstCmd=%v secondCmd=%v pending=%v", calls, firstCmd != nil, secondCmd != nil, second.actionPending)
		}
	})

	t.Run("git", func(t *testing.T) {
		m := NewGitStatusModel(DefaultStyles())
		m.SetStatus([]GitFileEntry{{FilePath: "main.go", Status: GitFileModified}}, "main", "", "")
		calls := 0
		m.SetActionCallback(func(GitAction, []string, string) { calls++ })
		first, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
		second, secondCmd := first.Update(tea.KeyMsg{Type: tea.KeySpace})
		if firstCmd == nil || calls != 1 || secondCmd != nil || !second.actionPending {
			t.Fatalf("git callbacks=%d firstCmd=%v secondCmd=%v pending=%v", calls, firstCmd != nil, secondCmd != nil, second.actionPending)
		}
	})

	t.Run("file browser", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "main.go")
		if err := os.WriteFile(file, []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m := NewFileBrowserModel(DefaultStyles())
		if err := m.SetPath(dir); err != nil {
			t.Fatal(err)
		}
		selectFileBrowserPath(t, &m, file)
		calls := 0
		m.SetActionCallback(func(FileBrowserAction, string, []string) { calls++ })
		first, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		second, secondCmd := first.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if firstCmd == nil || calls != 1 || secondCmd != nil || !second.actionPending {
			t.Fatalf("file callbacks=%d firstCmd=%v secondCmd=%v pending=%v", calls, firstCmd != nil, secondCmd != nil, second.actionPending)
		}
	})
}

func TestFileBrowserDirectoryEnterRepeatGuardIsExactAndBounded(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "folder")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "child.go")
	if err := os.WriteFile(file, []byte("package child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	openFolder := func(t *testing.T) FileBrowserModel {
		t.Helper()
		m := NewFileBrowserModel(DefaultStyles())
		if err := m.SetPath(root); err != nil {
			t.Fatal(err)
		}
		selectFileBrowserPath(t, &m, dir)
		opened, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || opened.currentDir != dir || opened.directoryNavGuardKey != "enter" || opened.directoryNavGuardUntil.IsZero() {
			t.Fatalf("folder open state: dir=%q cmd=%v guard=%q until=%v", opened.currentDir, cmd != nil, opened.directoryNavGuardKey, opened.directoryNavGuardUntil)
		}
		return opened
	}

	t.Run("same Enter repeat stays in folder", func(t *testing.T) {
		opened := openFolder(t)
		repeated, cmd := opened.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil || repeated.currentDir != dir {
			t.Fatalf("Enter auto-repeat left folder: dir=%q cmd=%v", repeated.currentDir, cmd != nil)
		}
	})

	t.Run("distinct key releases and continues", func(t *testing.T) {
		opened := openFolder(t)
		moved, _ := opened.Update(tea.KeyMsg{Type: tea.KeyDown})
		if moved.directoryNavGuardKey != "" || !moved.directoryNavGuardUntil.IsZero() {
			t.Fatalf("distinct key did not release directory guard: key=%q until=%v", moved.directoryNavGuardKey, moved.directoryNavGuardUntil)
		}
		if moved.selectedIndex == 0 {
			t.Fatal("distinct Down was swallowed instead of selecting the child")
		}
		calls := 0
		moved.SetActionCallback(func(FileBrowserAction, string, []string) { calls++ })
		_, cmd := moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 || cmd == nil {
			t.Fatalf("Enter after distinct navigation was blocked: calls=%d cmd=%v", calls, cmd != nil)
		}
	})

	t.Run("mouse wheel releases and continues", func(t *testing.T) {
		opened := openFolder(t)
		moved, _ := opened.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
		if moved.directoryNavGuardKey != "" || !moved.directoryNavGuardUntil.IsZero() {
			t.Fatalf("mouse wheel did not release directory guard: key=%q until=%v", moved.directoryNavGuardKey, moved.directoryNavGuardUntil)
		}
		if moved.selectedIndex == 0 {
			t.Fatal("mouse wheel did not select the child after releasing the guard")
		}
		calls := 0
		moved.SetActionCallback(func(FileBrowserAction, string, []string) { calls++ })
		_, cmd := moved.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if calls != 1 || cmd == nil {
			t.Fatalf("Enter after mouse navigation was blocked: calls=%d cmd=%v", calls, cmd != nil)
		}
	})

	t.Run("expired guard permits same Enter", func(t *testing.T) {
		opened := openFolder(t)
		opened.directoryNavGuardUntil = time.Now().Add(-time.Millisecond)
		returned, _ := opened.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if returned.currentDir != root {
			t.Fatalf("expired Enter guard still blocked intentional parent navigation: dir=%q want=%q", returned.currentDir, root)
		}
	})
}

func TestFileBrowserInternalNavigationDoesNotReleasePendingTerminalAction(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "folder")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.go"), []byte("package child\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{name: "parent key", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}}},
		{name: "home key", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'~'}}},
		{name: "mouse wheel", msg: tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewFileBrowserModel(DefaultStyles())
			if err := m.SetPath(dir); err != nil {
				t.Fatal(err)
			}
			calls := 0
			m.SetActionCallback(func(FileBrowserAction, string, []string) { calls++ })

			pending, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			if firstCmd == nil || calls != 1 || !pending.actionPending {
				t.Fatalf("initial close ownership: cmd=%v calls=%d pending=%v", firstCmd != nil, calls, pending.actionPending)
			}
			navigated, _ := pending.Update(tc.msg)
			if !navigated.actionPending {
				t.Fatal("internal navigation released the queued close ownership")
			}
			final, secondCmd := navigated.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
			if secondCmd != nil || calls != 1 || !final.actionPending {
				t.Fatalf("second close escaped ownership: cmd=%v calls=%d pending=%v", secondCmd != nil, calls, final.actionPending)
			}
		})
	}

	t.Run("authoritative SetPath starts a new ownership cycle", func(t *testing.T) {
		m := NewFileBrowserModel(DefaultStyles())
		if err := m.SetPath(dir); err != nil {
			t.Fatal(err)
		}
		calls := 0
		m.SetActionCallback(func(FileBrowserAction, string, []string) { calls++ })
		pending, firstCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		if firstCmd == nil || calls != 1 {
			t.Fatalf("initial close ownership: cmd=%v calls=%d", firstCmd != nil, calls)
		}
		if err := pending.SetPath(dir); err != nil {
			t.Fatal(err)
		}
		if pending.actionPending {
			t.Fatal("authoritative SetPath did not reset terminal ownership")
		}
		_, secondCmd := pending.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
		if secondCmd == nil || calls != 2 {
			t.Fatalf("new browser cycle remained blocked: cmd=%v calls=%d", secondCmd != nil, calls)
		}
	})
}
