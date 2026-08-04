//go:build !windows && !plan9

package context

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContextMemoryRepairsLegacyPrivateModes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(dir, sessionMemoryFilename): "session legacy",
		filepath.Join(dir, workingMemoryFilename): "working legacy",
	}
	for path, content := range paths {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	session := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	session.LoadFromDisk()
	if got := session.GetContent(); got != "session legacy" {
		t.Fatalf("session content = %q", got)
	}
	working := NewWorkingMemoryManager(root)
	working.LoadFromDisk()
	if got := working.GetContent(); got != "working legacy" {
		t.Fatalf("working content = %q", got)
	}

	assertContextMemoryMode(t, dir, 0o700)
	for path := range paths {
		assertContextMemoryMode(t, path, 0o600)
	}
}

func TestContextMemoryRejectsSymlinkedDirectoryWithoutTouchingTargets(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(targetDir, sessionMemoryFilename): "external session",
		filepath.Join(targetDir, workingMemoryFilename): "external working",
	}
	for path, content := range paths {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(targetDir, filepath.Join(root, ".gokin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	session := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
	session.LoadFromDisk()
	session.mu.Lock()
	session.content = "replacement session"
	session.mu.Unlock()
	session.writeToDisk()
	session.Clear()

	working := NewWorkingMemoryManager(root)
	working.LoadFromDisk()
	working.UpdateFromTurn(WorkingMemoryTurn{Response: "replacement working"})
	working.Clear()

	for path, want := range paths {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("external target %s changed: %q, %v", path, data, err)
		}
		assertContextMemoryMode(t, path, 0o644)
	}
	assertContextMemoryMode(t, targetDir, 0o755)
}

func TestContextMemoryRejectsSymlinkedFilesWithoutTouchingTargets(t *testing.T) {
	for _, tc := range []struct {
		name     string
		filename string
		operate  func(string)
	}{
		{
			name:     "session",
			filename: sessionMemoryFilename,
			operate: func(root string) {
				manager := NewSessionMemoryManager(root, DefaultSessionMemoryConfig())
				manager.LoadFromDisk()
				manager.mu.Lock()
				manager.content = "replacement"
				manager.mu.Unlock()
				manager.writeToDisk()
				manager.Clear()
			},
		},
		{
			name:     "working",
			filename: workingMemoryFilename,
			operate: func(root string) {
				manager := NewWorkingMemoryManager(root)
				manager.LoadFromDisk()
				manager.UpdateFromTurn(WorkingMemoryTurn{Response: "replacement"})
				manager.Clear()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".gokin")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "external")
			if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(dir, tc.filename)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			tc.operate(root)
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "keep" {
				t.Fatalf("external target changed: %q, %v", data, err)
			}
			assertContextMemoryMode(t, target, 0o644)
		})
	}
}

func assertContextMemoryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
