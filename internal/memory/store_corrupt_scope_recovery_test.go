package memory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func memoryStoreTestPaths(t *testing.T) (configDir, projectPath, memoryDir string) {
	t.Helper()
	configDir = t.TempDir()
	projectPath = t.TempDir()
	memoryDir = filepath.Join(configDir, "memory")
	if err := os.MkdirAll(memoryDir, 0700); err != nil {
		t.Fatalf("MkdirAll(memory): %v", err)
	}
	return configDir, projectPath, memoryDir
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes at %s changed: got %q, want %q", path, got, want)
	}
}

func TestCorruptGlobalIsBackedUpBeforeUnrelatedProjectFlush(t *testing.T) {
	configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
	globalPath := filepath.Join(memoryDir, "global.json")
	corrupt := []byte(`{"broken":`)
	if err := os.WriteFile(globalPath, corrupt, 0600); err != nil {
		t.Fatalf("WriteFile(global): %v", err)
	}

	store, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	backupPath := corruptMemoryBackupPath(globalPath, corrupt)
	assertFileBytes(t, backupPath, corrupt)

	project := NewEntry("project remains independently writable", MemoryProject).WithKey("project-safe")
	if err := store.Add(project); err != nil {
		t.Fatalf("Add(project): %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush(project): %v", err)
	}
	assertFileBytes(t, backupPath, corrupt)

	reloaded, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore(reload): %v", err)
	}
	if got, ok := reloaded.Get("project-safe"); !ok || got.Content != project.Content {
		t.Fatalf("reloaded project memory = %#v, ok=%v", got, ok)
	}
}

func TestCorruptGlobalCanBeRebuiltAfterBackup(t *testing.T) {
	configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
	globalPath := filepath.Join(memoryDir, "global.json")
	corrupt := []byte(`[not-json]`)
	if err := os.WriteFile(globalPath, corrupt, 0600); err != nil {
		t.Fatalf("WriteFile(global): %v", err)
	}

	store, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	backupPath := corruptMemoryBackupPath(globalPath, corrupt)
	assertFileBytes(t, backupPath, corrupt)

	global := NewEntry("preferred formatter is gofmt", MemoryGlobal).WithKey("formatter")
	if err := store.Add(global); err != nil {
		t.Fatalf("Add(global): %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush(global): %v", err)
	}
	assertFileBytes(t, backupPath, corrupt)

	reloaded, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore(reload): %v", err)
	}
	if got, ok := reloaded.Get("formatter"); !ok || got.Type != MemoryGlobal {
		t.Fatalf("reloaded global memory = %#v, ok=%v", got, ok)
	}
}

func TestInvalidMemoryArraysAreAtomicAndBackedUp(t *testing.T) {
	valid := NewEntry("valid prefix must not partially commit", MemoryGlobal).WithKey("valid-prefix")
	mixed, err := json.Marshal([]any{valid, nil})
	if err != nil {
		t.Fatalf("Marshal(mixed): %v", err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "null only", data: []byte(`[null]`)},
		{name: "valid then null", data: mixed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
			path := filepath.Join(memoryDir, "global.json")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			store, err := NewStore(configDir, projectPath, 10)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			if store.Count() != 0 {
				t.Fatalf("invalid array partially committed %d entries", store.Count())
			}
			assertFileBytes(t, corruptMemoryBackupPath(path, tc.data), tc.data)
		})
	}
}

func TestMemoryLoadRejectsUnknownAndWrongScopeTypes(t *testing.T) {
	tests := []struct {
		name        string
		projectFile bool
		entryType   MemoryType
	}{
		{name: "unknown", entryType: MemoryType("mystery")},
		{name: "project in global file", entryType: MemoryProject},
		{name: "global in project file", projectFile: true, entryType: MemoryGlobal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
			entry := NewEntry("invalid scope record", tc.entryType).WithKey("invalid")
			data, err := json.Marshal([]*Entry{entry})
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			path := filepath.Join(memoryDir, "global.json")
			if tc.projectFile {
				path = filepath.Join(memoryDir, hashPath(projectPath)+".json")
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			store, err := NewStore(configDir, projectPath, 10)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			if store.Count() != 0 {
				t.Fatalf("invalid type loaded %d entries", store.Count())
			}
			assertFileBytes(t, corruptMemoryBackupPath(path, data), data)
		})
	}
}

func TestOversizedMemoryFileIsQuarantinedWithoutReadingIntoMemory(t *testing.T) {
	configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
	globalPath := filepath.Join(memoryDir, "global.json")
	file, err := os.OpenFile(globalPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := file.Truncate(maxMemoryStoreFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := os.Stat(globalPath); !os.IsNotExist(err) {
		t.Fatalf("oversized source should have moved to quarantine, Stat err=%v", err)
	}
	backups, err := filepath.Glob(globalPath + ".oversized-*.bak")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("oversized backups = %v, want exactly one", backups)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Size() != maxMemoryStoreFileBytes+1 {
		t.Fatalf("quarantine Stat = %#v, err=%v", info, err)
	}

	global := NewEntry("replacement after oversized quarantine", MemoryGlobal).WithKey("replacement")
	if err := store.Add(global); err != nil {
		t.Fatalf("Add(global): %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Size() != maxMemoryStoreFileBytes+1 {
		t.Fatalf("quarantine changed after rebuild: info=%#v err=%v", info, err)
	}
}

func TestBackupFailureBlocksOnlyTargetScopeSynchronously(t *testing.T) {
	configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
	globalPath := filepath.Join(memoryDir, "global.json")
	corrupt := []byte(`{"unrecoverable":`)
	if err := os.WriteFile(globalPath, corrupt, 0600); err != nil {
		t.Fatalf("WriteFile(global): %v", err)
	}
	// Occupying the deterministic backup path with a directory makes the
	// atomic file replacement fail without changing directory permissions.
	if err := os.MkdirAll(corruptMemoryBackupPath(globalPath, corrupt), 0700); err != nil {
		t.Fatalf("MkdirAll(blocking backup): %v", err)
	}

	store, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, blocked := store.writeBlockedPaths[globalPath]; !blocked {
		t.Fatal("global scope was not write-blocked after backup failure")
	}
	if _, err := store.AddResolved(NewEntry("must be rejected", MemoryGlobal)); err == nil || !strings.Contains(err.Error(), "refusing to mutate") {
		t.Fatalf("AddResolved(global) error = %v, want synchronous policy failure", err)
	}

	seed := NewEntry("seeded blocked entry", MemoryGlobal).WithKey("blocked")
	store.mu.Lock()
	store.globalEntries[seed.ID] = cloneEntry(seed)
	store.indexEntryKeyLocked(seed)
	store.mu.Unlock()
	if err := store.Edit(seed.ID, "changed"); err == nil {
		t.Fatal("Edit succeeded for blocked global scope")
	}
	if store.RecordFeedback(seed.ID, true) {
		t.Fatal("RecordFeedback succeeded for blocked global scope")
	}
	store.RecordAccess(seed.ID)
	if got, _ := store.GetByID(seed.ID); got.AccessCount != 0 || got.Content != seed.Content {
		t.Fatalf("blocked entry mutated: %#v", got)
	}
	if store.Remove(seed.ID) {
		t.Fatal("Remove succeeded for blocked global scope")
	}
	if err := store.Clear(); err == nil {
		t.Fatal("Clear succeeded while one of its target scopes was blocked")
	}
	importData, _ := json.Marshal([]*Entry{NewEntry("imported", MemoryGlobal)})
	if err := store.Import(importData); err == nil {
		t.Fatal("Import(global) succeeded for blocked global scope")
	}

	project := NewEntry("healthy project still persists", MemoryProject).WithKey("healthy")
	if err := store.Add(project); err != nil {
		t.Fatalf("Add(project): %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush(healthy project): %v", err)
	}
	assertFileBytes(t, globalPath, corrupt)

	reloaded, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore(reload): %v", err)
	}
	if got, ok := reloaded.Get("healthy"); !ok || got.Content != project.Content {
		t.Fatalf("healthy project did not persist: got=%#v ok=%v", got, ok)
	}
}

func TestSessionTTLIsNeverPersistedInProjectArchive(t *testing.T) {
	configDir, projectPath, memoryDir := memoryStoreTestPaths(t)
	entry := NewEntry("temporary session investigation", MemorySession).WithKey("session-ttl")
	entry.ExpiresAt = time.Now().Add(50 * time.Millisecond)
	store, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Add(entry); err != nil {
		t.Fatalf("Add(session): %v", err)
	}
	time.Sleep(90 * time.Millisecond)
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	reloaded, err := NewStore(configDir, projectPath, 10)
	if err != nil {
		t.Fatalf("NewStore(reload): %v", err)
	}
	if _, ok := reloaded.GetByID(entry.ID); ok {
		t.Fatal("expired session entry survived process reload")
	}
	backups, err := filepath.Glob(filepath.Join(memoryDir, "*.corrupt-*.bak"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("Store quarantined its own session archive: %v", backups)
	}
}
