package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/memory"
)

// findMemoryJSONContaining globs configDir/memory/*.json (Store.storagePath
// hashes the project path into the filename, which this test doesn't need
// to reproduce) and returns the content of whichever file contains needle.
func findMemoryJSONContaining(t *testing.T, configDir, needle string) (string, bool) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(configDir, "memory", "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), needle) {
			return path, true
		}
	}
	return "", false
}

// TestGracefulShutdown_FlushesMemoryStore (round 5) pins the fix: the
// `memory` tool's kv store (remember/recall/forget) was the only persisted
// store NOT flushed on graceful shutdown — errorStore/exampleStore were,
// despite Store.Flush() existing and being the exact same "debounced-save
// might not fire before process exit" concern. Store.Add schedules a 2s
// debounced save; a user's last instruction being "remember X" followed by
// quitting within that window silently lost the fact (the tool reported
// success, but the process exited before the debounce timer fired).
func TestGracefulShutdown_FlushesMemoryStore(t *testing.T) {
	configDir := t.TempDir()
	projectPath := t.TempDir()

	store, err := memory.NewStore(configDir, projectPath, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	entry := memory.NewEntry("the user's favorite editor is vim", memory.MemoryProject)
	if err := store.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Confirm the debounced save hasn't fired yet (2s delay) — proves that
	// whatever we find on disk after shutdown came from Flush(), not from
	// the background timer racing ahead of us.
	if _, found := findMemoryJSONContaining(t, configDir, "favorite editor"); found {
		t.Fatal("test setup invalid: the debounced save already fired before shutdown ran — increase test speed or the store's debounce changed")
	}

	a := &App{memoryStore: store}
	a.gracefulShutdown(context.Background())

	if _, found := findMemoryJSONContaining(t, configDir, "favorite editor"); !found {
		t.Fatal("expected the memory store to be flushed to disk during shutdown, but no memory/*.json file contains the remembered fact")
	}
}
