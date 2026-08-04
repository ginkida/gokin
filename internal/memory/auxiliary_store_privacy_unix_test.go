//go:build !windows && !plan9

package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuxiliaryStoresRepairPrivateModes(t *testing.T) {
	configDir := t.TempDir()
	dir := filepath.Join(configDir, "memory")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		filepath.Join(dir, "examples.json"): `{"example":{"id":"example","task_type":"test"}}`,
		filepath.Join(dir, "errors.json"):   `[{"id":"error","error_type":"test"}]`,
	}
	for path, data := range paths {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewExampleStore(configDir); err != nil {
		t.Fatal(err)
	}
	if _, err := NewErrorStore(configDir); err != nil {
		t.Fatal(err)
	}
	assertAuxiliaryMode(t, dir, 0o700)
	for path := range paths {
		assertAuxiliaryMode(t, path, 0o600)
	}
}

func TestAuxiliaryStoreRejectsSymlinkedDirectory(t *testing.T) {
	configDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(configDir, "memory")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewExampleStore(configDir); err == nil {
		t.Fatal("NewExampleStore accepted a symlinked memory directory")
	}
	if _, err := NewErrorStore(configDir); err == nil {
		t.Fatal("NewErrorStore accepted a symlinked memory directory")
	}
	assertAuxiliaryMode(t, target, 0o755)
}

func assertAuxiliaryMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
