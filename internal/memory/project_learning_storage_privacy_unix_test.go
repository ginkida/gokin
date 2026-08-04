//go:build !windows && !plan9

package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectLearningRepairsPrivateStorageModes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(dir, "learning.yaml"):     "preferences:\n  test: private\n",
		filepath.Join(dir, "project-memory.md"): "# Project Memory\n",
	}
	for path, content := range paths {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pl, err := NewProjectLearning(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := pl.GetPreference("test"); got != "private" {
		t.Fatalf("loaded preference = %q", got)
	}
	assertProjectLearningMode(t, dir, 0o700)
	for path := range paths {
		assertProjectLearningMode(t, path, 0o600)
	}

	pl.SetPreference("new", "value")
	if err := pl.Flush(); err != nil {
		t.Fatal(err)
	}
	for path := range paths {
		assertProjectLearningMode(t, path, 0o600)
	}
}

func TestProjectLearningRejectsSymlinkedStorage(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "external")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, ".gokin")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewProjectLearning(root); err == nil {
			t.Fatal("NewProjectLearning accepted a symlinked .gokin directory")
		}
		assertProjectLearningMode(t, target, 0o755)
	})

	for _, name := range []string{"learning.yaml", "project-memory.md"} {
		t.Run(name, func(t *testing.T) {
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
			if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := NewProjectLearning(root); err == nil {
				t.Fatalf("NewProjectLearning accepted symlinked %s", name)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "keep" {
				t.Fatalf("symlink target changed: %q, %v", data, err)
			}
			assertProjectLearningMode(t, target, 0o644)
		})
	}
}

func assertProjectLearningMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
