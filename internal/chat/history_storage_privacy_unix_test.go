//go:build !windows

package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryManagerHardensExistingDirectoryAndFileModes(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	historyDir := filepath.Join(xdg, "gokin", "history")
	if err := os.MkdirAll(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(historyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	assertMode(t, historyDir, 0o700)

	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state := SessionState{ID: "private"}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, "private.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.LoadFull("private"); err != nil {
		t.Fatal(err)
	}
	assertMode(t, sessionsDir, 0o700)
	assertMode(t, path, 0o600)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
