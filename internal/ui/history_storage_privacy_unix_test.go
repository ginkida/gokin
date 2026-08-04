//go:build !windows && !plan9

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInputHistoryLoadRepairsLegacyPermissions(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	dir := filepath.Join(dataDir, "gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFile)
	if err := os.WriteFile(path, []byte("legacy prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := NewInputModel(DefaultStyles(), t.TempDir())
	if err := m.LoadHistory(); err != nil {
		t.Fatal(err)
	}
	assertHistoryMode(t, dir, 0o700)
	assertHistoryMode(t, path, 0o600)
}

func TestInputHistorySaveRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	dir := filepath.Join(dataDir, "gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, historyFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetHistory([]string{"replacement"})
	if err := m.SaveHistory(); err == nil {
		t.Fatal("SaveHistory unexpectedly accepted a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertHistoryMode(t, target, 0o644)
}

func TestCommandHistoryLoadRepairsLegacyPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFileName)
	if err := os.WriteFile(path, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	history := &CommandHistory{entries: make(map[string]*HistoryEntry), filePath: path}
	if err := history.load(); err != nil {
		t.Fatal(err)
	}
	assertHistoryMode(t, dir, 0o700)
	assertHistoryMode(t, path, 0o600)
}

func TestCommandHistorySaveRejectsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	targetDir := t.TempDir()
	dir := filepath.Join(root, "gokin")
	if err := os.Symlink(targetDir, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	history := &CommandHistory{
		entries:  map[string]*HistoryEntry{"cmd": {Command: "cmd", Count: 1}},
		filePath: filepath.Join(dir, historyFileName),
	}
	if err := history.Flush(); err == nil {
		t.Fatal("Flush unexpectedly accepted a symlinked history directory")
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target directory changed: %v", entries)
	}
}

func assertHistoryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
