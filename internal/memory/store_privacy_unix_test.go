//go:build !windows && !plan9

package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func assertOwnerOnlyMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func TestStoreRepairsPrivateDirectoryAndJSONModes(t *testing.T) {
	configDir := t.TempDir()
	projectDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}

	projectHash := hashPath(projectDir)
	paths := []string{
		filepath.Join(memoryDir, projectHash+".json"),
		filepath.Join(memoryDir, "global.json"),
		filepath.Join(memoryDir, projectHash+".archive.json"),
		filepath.Join(memoryDir, "global.archive.json"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Umask can only narrow creation; force the legacy/public mode so the
		// regression proves NewStore repairs an existing installation.
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	store, err := NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	assertOwnerOnlyMode(t, memoryDir, 0o700)
	for _, path := range paths {
		assertOwnerOnlyMode(t, path, 0o600)
	}

	// Prove every AtomicWrite path also enforces 0600, rather than relying only
	// on the one-time migration above.
	for _, path := range paths {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Add(NewEntry("private project note", MemoryProject)); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(NewEntry("private global preference", MemoryGlobal)); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, path := range paths {
		assertOwnerOnlyMode(t, path, 0o600)
	}
}
