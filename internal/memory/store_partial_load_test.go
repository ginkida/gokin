package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCorruptProjectFileDoesNotDiscardValidGlobalMemory(t *testing.T) {
	configDir := t.TempDir()
	projectDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	global := NewEntry("global preference survives", MemoryGlobal).WithKey("global-preference")
	globalJSON, err := json.Marshal([]*Entry{global})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "global.json"), globalJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, hashPath(projectDir)+".json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatalf("partial corruption should be non-fatal: %v", err)
	}
	if got, ok := store.Get("global-preference"); !ok || got.ID != global.ID {
		t.Fatalf("valid global scope was discarded with corrupt project scope: %+v, %v", got, ok)
	}

	if err := store.Add(NewEntry("new project fact", MemoryProject).WithKey("project-fact")); err != nil {
		t.Fatal(err)
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get("global-preference"); !ok {
		t.Fatal("later project save overwrote valid global memory after partial load")
	}
	if _, ok := reopened.Get("project-fact"); !ok {
		t.Fatal("project scope did not recover after replacing corrupt JSON")
	}
}
