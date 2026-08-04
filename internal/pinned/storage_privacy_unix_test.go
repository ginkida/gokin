//go:build !windows

package pinned

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRepairsLegacyPermissions(t *testing.T) {
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte("secret pin"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := Load(workDir); err != nil || got != "secret pin" {
		t.Fatalf("Load = %q, %v", got, err)
	}
	assertMode(t, dir, 0o700)
	assertMode(t, path, 0o600)
}

func TestSaveRejectsSymlinkedStorageDirectory(t *testing.T) {
	workDir := t.TempDir()
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(workDir, ".gokin")); err != nil {
		t.Fatal(err)
	}
	if err := Save(workDir, "secret pin"); err == nil {
		t.Fatal("Save unexpectedly accepted symlinked .gokin directory")
	}
	if entries, err := os.ReadDir(targetDir); err != nil || len(entries) != 0 {
		t.Fatalf("symlink target changed: entries=%v err=%v", entries, err)
	}
}

func TestSaveRejectsSymlinkedPinFile(t *testing.T) {
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, fileName)); err != nil {
		t.Fatal(err)
	}

	if err := Save(workDir, "replacement"); err == nil {
		t.Fatal("Save unexpectedly accepted symlinked pin file")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("symlink target = %q, want unchanged", data)
	}
	assertMode(t, target, 0o644)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
