package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- Store: archive lifecycle ---

func TestArchiveExpiredLocked_RemovesExpiredEntries(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{ID: "exp1", Content: "temp", Type: MemoryProject, Project: store.projectHash, ExpiresAt: time.Now().Add(-time.Hour)}
	store.entries[e.ID] = e
	store.mu.Unlock()

	store.mu.Lock()
	store.archiveExpiredLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.archived["exp1"]; !ok {
		t.Error("expired entry should be in archived map")
	}
	if _, ok := store.entries["exp1"]; ok {
		t.Error("expired entry should be removed from entries")
	}
}

func TestArchiveExpiredLocked_GlobalEntry(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{ID: "gexp", Content: "global temp", Type: MemoryGlobal, ExpiresAt: time.Now().Add(-time.Hour)}
	store.globalEntries[e.ID] = e
	store.mu.Unlock()

	store.mu.Lock()
	store.archiveExpiredLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.globalArchive["gexp"]; !ok {
		t.Error("expired global entry should be in globalArchive")
	}
}

func TestArchiveStaleLowValueLocked_ArchivesOldUnused(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{
		ID:        "stale1",
		Content:   "old unused fact",
		Type:      MemoryProject,
		Project:   store.projectHash,
		Timestamp: time.Now().Add(-staleArchiveWindow - time.Hour),
	}
	store.entries[e.ID] = e
	store.mu.Unlock()

	store.mu.Lock()
	store.archiveStaleLowValueLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.archived["stale1"]; !ok {
		t.Error("stale low-value entry should be archived")
	}
}

func TestArchiveStaleLowValueLocked_SkipsSessionType(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{
		ID:        "sess1",
		Content:   "session note",
		Type:      MemorySession,
		Project:   store.projectHash,
		Timestamp: time.Now().Add(-staleArchiveWindow - time.Hour),
	}
	store.entries[e.ID] = e
	store.mu.Unlock()

	store.mu.Lock()
	store.archiveStaleLowValueLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.entries["sess1"]; !ok {
		t.Error("session entries should NOT be archived by stale-low-value")
	}
}

func TestArchiveStaleLowValueLocked_SkipsReinforced(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{
		ID:            "reinforced1",
		Content:       "reinforced fact",
		Type:          MemoryProject,
		Project:       store.projectHash,
		Timestamp:     time.Now().Add(-staleArchiveWindow - time.Hour),
		Reinforcement: 2,
	}
	store.entries[e.ID] = e
	store.mu.Unlock()

	store.mu.Lock()
	store.archiveStaleLowValueLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.entries["reinforced1"]; !ok {
		t.Error("reinforced entry should NOT be archived")
	}
}

func TestArchiveStaleLowValueLocked_SkipsAccessed(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{
		ID:          "accessed1",
		Content:     "accessed fact",
		Type:        MemoryProject,
		Project:     store.projectHash,
		Timestamp:   time.Now().Add(-staleArchiveWindow - time.Hour),
		AccessCount: 3,
	}
	store.entries[e.ID] = e
	store.mu.Unlock()

	store.mu.Lock()
	store.archiveStaleLowValueLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.entries["accessed1"]; !ok {
		t.Error("accessed entry should NOT be archived")
	}
}

func TestArchiveEntryLocked_GlobalEntry(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{ID: "garch", Content: "global", Type: MemoryGlobal, Key: "gk"}
	store.globalEntries[e.ID] = e
	store.byKey["gk"] = e.ID
	store.archiveEntryLocked(e, "test_reason")
	store.mu.Unlock()

	if _, ok := store.globalArchive["garch"]; !ok {
		t.Error("global entry should be in globalArchive")
	}
	if _, ok := store.byKey["gk"]; ok {
		t.Error("byKey should be cleaned for archived entry")
	}
	if !e.Archived {
		t.Error("entry.Archived should be true")
	}
	if e.ArchiveReason != "test_reason" {
		t.Errorf("ArchiveReason = %q, want %q", e.ArchiveReason, "test_reason")
	}
}

func TestArchiveEntryLocked_NilSafe(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	store.archiveEntryLocked(nil, "nil_test")
	count := len(store.archived) + len(store.globalArchive)
	store.mu.Unlock()
	if count != 0 {
		t.Errorf("nil entry should not be added to archive, count=%d", count)
	}
}

// --- Store: cleanupArchivedLocked ---

func TestCleanupArchivedLocked_RemovesOldArchiveEntries(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{
		ID:        "old-archived",
		Content:   "very old",
		Type:      MemoryProject,
		Timestamp: time.Now().Add(-400 * 24 * time.Hour),
		Key:       "oldk",
	}
	store.archived[e.ID] = e
	store.byKey["oldk"] = e.ID
	store.cleanupArchivedLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.archived["old-archived"]; ok {
		t.Error("entry older than 365 days should be cleaned from archive")
	}
	if _, ok := store.byKey["oldk"]; ok {
		t.Error("byKey should be cleaned for purged archive entry")
	}
}

func TestCleanupArchivedLocked_RemovesNilEntries(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	store.archived["nil-id"] = nil
	store.cleanupArchivedLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.archived["nil-id"]; ok {
		t.Error("nil entry should be removed from archive")
	}
}

func TestCleanupArchivedLocked_RemovesExpiredBeyondGrace(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	e := &Entry{
		ID:        "expired-archived",
		Content:   "expired long ago",
		Type:      MemoryProject,
		Timestamp: time.Now(),
		ExpiresAt: time.Now().Add(-31 * 24 * time.Hour), // expired >30 days ago
	}
	store.archived[e.ID] = e
	store.cleanupArchivedLocked(time.Now())
	store.mu.Unlock()

	if _, ok := store.archived["expired-archived"]; ok {
		t.Error("entry expired >30 days ago should be purged from archive")
	}
}

// --- Store: pruneOldest ---

func TestPruneOldest_RemovesOldestEntries(t *testing.T) {
	store, err := NewStore(t.TempDir(), "/test/project", 3)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	store.mu.Lock()
	for i := range 5 {
		e := &Entry{
			ID:        "old" + string(rune('A'+i)),
			Content:   "entry " + string(rune('A'+i)),
			Type:      MemoryProject,
			Project:   store.projectHash,
			Timestamp: time.Now().Add(time.Duration(i) * time.Hour),
			Key:       "key" + string(rune('A'+i)),
		}
		store.entries[e.ID] = e
		store.byKey[e.Key] = e.ID
	}
	store.pruneOldest()
	store.mu.Unlock()

	total := len(store.entries) + len(store.globalEntries)
	if total != 3 {
		t.Errorf("after prune, total = %d, want 3", total)
	}
}

func TestPruneOldest_NoLimit(t *testing.T) {
	store, err := NewStore(t.TempDir(), "/test/project", 0)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.mu.Lock()
	store.entries["x"] = &Entry{ID: "x", Type: MemoryProject}
	store.pruneOldest()
	store.mu.Unlock()

	if _, ok := store.entries["x"]; !ok {
		t.Error("maxEntries=0 should mean unlimited, entry should survive")
	}
}

// --- Store: save / Flush ---

func TestStoreSave_PersistsToDisk(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("persist me", MemoryProject).WithKey("pk"))

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	data, err := os.ReadFile(store.storagePath())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "persist me") {
		t.Error("persisted file should contain the entry content")
	}
}

func TestStoreFlush_NoDirtyReturnsNil(t *testing.T) {
	store := newTestStore(t)
	// Nothing added, nothing dirty
	if err := store.Flush(); err != nil {
		t.Errorf("Flush on clean store should return nil, got %v", err)
	}
}

func TestStoreFlush_AfterSaveIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("once", MemoryProject).WithKey("ok"))

	if err := store.Flush(); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	// Second flush should be a no-op (dirty=false)
	if err := store.Flush(); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
}

// --- Store: load / loadFile error paths ---

func TestStoreLoadFile_CorruptJSON(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)

	// Write corrupt JSON to a project file — the hash for "/test/project"
	// is deterministic, so we just create the store once to discover it.
	store, err := NewStore(dir, "/test/project", 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	corruptPath := store.storagePath()
	os.WriteFile(corruptPath, []byte("{not valid json"), 0644)

	// Reload — load should fail silently (non-fatal)
	store2, err := NewStore(dir, "/test/project", 100)
	if err != nil {
		t.Fatalf("NewStore with corrupt file should not return error: %v", err)
	}
	if store2.Count() != 0 {
		t.Errorf("Count after corrupt load = %d, want 0", store2.Count())
	}
}

// --- Store: Search with query text (scoreEntry) ---

func TestSearch_QueryText_RanksByKeyMatch(t *testing.T) {
	store := newTestStore(t)
	e1 := NewEntry("deploy instructions", MemoryProject).WithKey("deploy")
	e2 := NewEntry("unrelated fact", MemoryProject).WithKey("other")
	store.Add(e1)
	store.Add(e2)

	results := store.Search(SearchQuery{Query: "deploy"})
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'deploy'")
	}
	if results[0].Key != "deploy" {
		t.Errorf("top result key = %q, want 'deploy'", results[0].Key)
	}
}

func TestSearch_QueryText_RanksByTagMatch(t *testing.T) {
	store := newTestStore(t)
	e1 := NewEntry("some content here", MemoryProject).WithTags([]string{"go", "testing"})
	e2 := NewEntry("other content", MemoryProject).WithTags([]string{"python"})
	store.Add(e1)
	store.Add(e2)

	results := store.Search(SearchQuery{Query: "testing"})
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'testing'")
	}
	if !results[0].HasTag("testing") {
		t.Errorf("top result should have tag 'testing', tags=%v", results[0].Tags)
	}
}

func TestSearch_LimitTruncatesResults(t *testing.T) {
	store := newTestStore(t)
	for i := range 5 {
		store.Add(NewEntry("shared content "+string(rune('A'+i)), MemoryProject).WithKey("k" + string(rune('A'+i))))
	}

	results := store.Search(SearchQuery{Query: "shared", Limit: 2})
	if len(results) > 2 {
		t.Errorf("results = %d, want <= 2", len(results))
	}
}

func TestSearch_IncludeArchived(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("archived content", MemoryProject).WithKey("ak")
	store.Add(e)

	store.mu.Lock()
	store.archiveEntryLocked(store.entries[e.ID], "manual")
	delete(store.entries, e.ID)
	store.mu.Unlock()

	// Without IncludeArchived
	results := store.Search(SearchQuery{Query: "archived"})
	for _, r := range results {
		if r.ID == e.ID {
			t.Error("archived entry should not appear without IncludeArchived")
		}
	}

	// With IncludeArchived
	results = store.Search(SearchQuery{Query: "archived", IncludeArchived: true})
	found := false
	for _, r := range results {
		if r.ID == e.ID {
			found = true
		}
	}
	if !found {
		t.Error("archived entry should appear with IncludeArchived=true")
	}
}

// --- Store: GetForContext with cache ---

func TestGetForContext_CacheInvalidatedOnMutation(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("first fact", MemoryProject).WithKey("fk"))

	c1 := store.GetForContext(false)
	if !strings.Contains(c1, "first fact") {
		t.Errorf("first GetForContext missing fact: %q", c1)
	}

	store.Add(NewEntry("second fact", MemoryProject).WithKey("sk"))

	c2 := store.GetForContext(false)
	if !strings.Contains(c2, "second fact") {
		t.Errorf("GetForContext after mutation missing new fact: %q", c2)
	}
}

func TestGetForContext_ProjectOnlyExcludesGlobal(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("project fact", MemoryProject).WithKey("pk"))
	store.Add(NewEntry("global fact", MemoryGlobal).WithKey("gk"))

	result := store.GetForContext(true)
	if strings.Contains(result, "global fact") {
		t.Error("GetForContext(true) should not include global entries")
	}
	if !strings.Contains(result, "project fact") {
		t.Error("GetForContext(true) should include project entries")
	}
}

// --- Store: Get expired ---

func TestGet_ExpiredEntryReturnsFalse(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("temp", MemorySession).WithKey("tk")
	e.ExpiresAt = time.Now().Add(-time.Hour)
	store.Add(e)

	if _, ok := store.Get("tk"); ok {
		t.Error("Get should return false for expired entry")
	}
}

func TestGet_GlobalExpiredReturnsFalse(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("global temp", MemoryGlobal).WithKey("gtk")
	e.ExpiresAt = time.Now().Add(-time.Hour)
	store.Add(e)

	if _, ok := store.Get("gtk"); ok {
		t.Error("Get should return false for expired global entry")
	}
}

// --- Store: Add semantic dedup ---

func TestAdd_SemanticDuplicateReinforces(t *testing.T) {
	store := newTestStore(t)
	e1 := NewEntry("the deploy key rotates every 90 days", MemoryProject).WithKey("dk")
	store.Add(e1)

	// Near-duplicate content
	e2 := NewEntry("the deploy key rotates every 90 days", MemoryProject).WithKey("dk")
	store.Add(e2)

	store.mu.RLock()
	got := store.entries[e1.ID]
	store.mu.RUnlock()

	if got.Reinforcement != 1 {
		t.Errorf("Reinforcement = %d, want 1", got.Reinforcement)
	}
}

func TestAdd_KeyReplacesOldEntry(t *testing.T) {
	store := newTestStore(t)
	e1 := NewEntry("original content", MemoryProject).WithKey("shared-key")
	store.Add(e1)

	// Different content, same key → should replace
	e2 := NewEntry("replacement content", MemoryProject).WithKey("shared-key")
	e2.ID = "custom-id-2"
	store.Add(e2)

	got, ok := store.Get("shared-key")
	if !ok {
		t.Fatal("entry with shared-key should exist")
	}
	if got.Content != "replacement content" {
		t.Errorf("Content = %q, want 'replacement content'", got.Content)
	}
}

// --- Store: Remove from archive ---

func TestRemove_FromArchive(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("archived entry", MemoryProject).WithKey("ark")
	store.Add(e)

	store.mu.Lock()
	store.archiveEntryLocked(store.entries[e.ID], "test")
	delete(store.entries, e.ID)
	store.mu.Unlock()

	if !store.Remove(e.ID) {
		t.Error("Remove should return true for archived entry")
	}
}

func TestRemove_FromGlobalArchive(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("global archived", MemoryGlobal).WithKey("gark")
	store.Add(e)

	store.mu.Lock()
	store.archiveEntryLocked(store.globalEntries[e.ID], "test")
	delete(store.globalEntries, e.ID)
	store.mu.Unlock()

	if !store.Remove(e.ID) {
		t.Error("Remove should return true for global archived entry")
	}
}

// --- Store: Import with duplicate ---

func TestImport_SkipsDuplicateByID(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("existing", MemoryProject).WithKey("ik")
	store.Add(e)

	data, _ := store.Export()
	// Import the same data back — duplicates should be skipped
	if err := store.Import(data); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Count should not double
	count := 0
	store.mu.RLock()
	for range store.entries {
		count++
	}
	store.mu.RUnlock()
	if count != 1 {
		t.Errorf("entries count = %d, want 1 (duplicate should be skipped)", count)
	}
}

func TestImport_InvalidJSON(t *testing.T) {
	store := newTestStore(t)
	err := store.Import([]byte("not valid json"))
	if err == nil {
		t.Error("Import with invalid JSON should return error")
	}
}

// --- Store: NewStore load failure ---

func TestNewStore_MkdirFailure(t *testing.T) {
	// Use a path under a file (not a dir) to force MkdirAll failure
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "iamfile")
	os.WriteFile(filePath, []byte("x"), 0644)

	_, err := NewStore(filepath.Join(filePath, "subdir"), "/test/project", 100)
	if err == nil {
		t.Error("NewStore should fail when MkdirAll fails")
	}
}

// --- Store: generateID uniqueness ---

func TestGenerateID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool, 1000)
	for range 1000 {
		id := generateID("same content")
		if ids[id] {
			t.Fatalf("collision detected for generateID with same content: %s", id)
		}
		ids[id] = true
	}
}

func TestGenerateID_Format(t *testing.T) {
	id := generateID("test")
	if !strings.HasPrefix(id, "mem_") {
		t.Errorf("ID = %q, want 'mem_' prefix", id)
	}
	// mem_ + 8 hex bytes = mem_ + 16 chars
	if len(id) != 4+16 {
		t.Errorf("ID length = %d, want %d", len(id), 4+16)
	}
}
