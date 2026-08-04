//go:build !windows && !plan9

package loops

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStorageRepairsPrivateModes(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "loop-private.json")
	loop := &Loop{ID: "loop-private", Task: "private task", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, CreatedAt: time.Now()}
	data, err := loop.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, errs := NewFileStorage(dir).Load()
	if len(errs) != 0 || len(loaded) != 1 {
		t.Fatalf("Load = %d loops, %v", len(loaded), errs)
	}
	assertLoopMode(t, dir, 0o700)
	assertLoopMode(t, path, 0o600)
}

func TestFileStorageRejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "loops")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	store := NewFileStorage(link)
	loop := &Loop{ID: "loop-private", Task: "private task", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, CreatedAt: time.Now()}
	if err := store.Save(loop); err == nil {
		t.Fatal("Save accepted symlinked loops directory")
	}
	if _, errs := store.Load(); len(errs) == 0 {
		t.Fatal("Load accepted symlinked loops directory")
	}
	assertLoopMode(t, target, 0o755)
}

func assertLoopMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
