//go:build !windows && !plan9

package securefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateReadWriteRepairsModeAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.lock")
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	file, err := OpenPrivateReadWrite(path)
	if err != nil {
		t.Fatalf("OpenPrivateReadWrite(existing): %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertSecureFSMode(t, path, 0o600)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "external")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if file, err := OpenPrivateReadWrite(path); err == nil {
		_ = file.Close()
		t.Fatal("OpenPrivateReadWrite accepted a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertSecureFSMode(t, target, 0o644)
}

func assertSecureFSMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
