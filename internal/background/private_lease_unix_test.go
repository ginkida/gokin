//go:build !windows && !plan9

package background

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackgroundLeaseRepairsModeAndRejectsSymlink(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	path, err := store.lockPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireWorkerLease(id)
	if err != nil {
		t.Fatalf("AcquireWorkerLease(existing): %v", err)
	}
	assertBackgroundMode(t, path, 0o600)
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.AcquireWorkerLease(id); err == nil {
		t.Fatal("AcquireWorkerLease accepted a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertBackgroundMode(t, target, 0o644)
}

func TestBackgroundStoreRejectsSymlinkedPrivateSubdirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "jobs")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewStoreAt(root); err == nil {
		t.Fatal("NewStoreAt accepted a symlinked jobs directory")
	}
	assertBackgroundMode(t, target, 0o755)
}

func assertBackgroundMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
