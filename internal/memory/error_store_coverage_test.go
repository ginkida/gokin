package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- GetErrorContext ---

func TestErrorStore_GetErrorContext_ReturnsFormattedString(t *testing.T) {
	es := newTestErrorStore(t)
	es.LearnError("build", "undefined: foo", "declare foo", []string{"go"})

	ctx := es.GetErrorContext("error: undefined: foo at line 10")
	if ctx == "" {
		t.Fatal("expected non-empty context for matching error")
	}
	if !strings.Contains(ctx, "Learned from Previous Errors") {
		t.Error("context should contain header")
	}
	if !strings.Contains(ctx, "declare foo") {
		t.Error("context should contain the solution")
	}
}

func TestErrorStore_GetErrorContext_NoMatch(t *testing.T) {
	es := newTestErrorStore(t)
	es.LearnError("build", "undefined: foo", "declare foo", []string{"go"})

	ctx := es.GetErrorContext("completely unrelated error message")
	if ctx != "" {
		t.Errorf("expected empty context for non-matching error, got %q", ctx)
	}
}

func TestErrorStore_GetErrorContext_LimitsToThreeMatches(t *testing.T) {
	es := newTestErrorStore(t)
	// Learn 5 errors that all match the same substring
	for i := range 5 {
		es.LearnError("build", "common-pattern-"+string(rune('A'+i)), "solution "+string(rune('A'+i)), nil)
	}

	ctx := es.GetErrorContext("common-pattern-A common-pattern-B common-pattern-C common-pattern-D common-pattern-E")
	// Count how many "### " headers appear (each match has one)
	count := strings.Count(ctx, "### ")
	if count > 3 {
		t.Errorf("context should show at most 3 matches, got %d", count)
	}
}

// --- RecordSuccess / RecordFailure ---

func TestErrorStore_RecordSuccess_IncreasesRate(t *testing.T) {
	es := newTestErrorStore(t)
	err := es.LearnError("build", "undefined: foo", "declare foo", nil)
	if err != nil {
		t.Fatalf("LearnError: %v", err)
	}

	var entryID string
	for id := range es.entries {
		entryID = id
		break
	}

	before := es.entries[entryID].SuccessRate
	if err := es.RecordSuccess(entryID); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}
	after := es.entries[entryID].SuccessRate

	if after <= before {
		t.Errorf("SuccessRate should increase: before=%f after=%f", before, after)
	}
	if es.entries[entryID].UseCount != 1 {
		t.Errorf("UseCount = %d, want 1", es.entries[entryID].UseCount)
	}
}

func TestErrorStore_RecordFailure_DecreasesRate(t *testing.T) {
	es := newTestErrorStore(t)
	es.LearnError("build", "undefined: foo", "declare foo", nil)

	var entryID string
	for id := range es.entries {
		entryID = id
		break
	}

	before := es.entries[entryID].SuccessRate
	if err := es.RecordFailure(entryID); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	after := es.entries[entryID].SuccessRate

	if after >= before {
		t.Errorf("SuccessRate should decrease: before=%f after=%f", before, after)
	}
}

func TestErrorStore_RecordSuccess_NotFound(t *testing.T) {
	es := newTestErrorStore(t)
	err := es.RecordSuccess("nonexistent")
	if err == nil {
		t.Error("RecordSuccess should return error for non-existent entry")
	}
}

func TestErrorStore_RecordFailure_NotFound(t *testing.T) {
	es := newTestErrorStore(t)
	err := es.RecordFailure("nonexistent")
	if err == nil {
		t.Error("RecordFailure should return error for non-existent entry")
	}
}

// --- GetByType ---

func TestErrorStore_GetByType_ReturnsEntries(t *testing.T) {
	es := newTestErrorStore(t)
	es.LearnError("build", "error1", "solution1", nil)
	es.LearnError("test", "error2", "solution2", nil)
	es.LearnError("build", "error3", "solution3", nil)

	buildErrors := es.GetByType("build")
	if len(buildErrors) != 2 {
		t.Errorf("GetByType('build') = %d entries, want 2", len(buildErrors))
	}
}

func TestErrorStore_GetByType_NoMatch(t *testing.T) {
	es := newTestErrorStore(t)
	es.LearnError("build", "error1", "solution1", nil)

	result := es.GetByType("nonexistent-type")
	if len(result) != 0 {
		t.Errorf("GetByType for non-existent type = %d, want 0", len(result))
	}
}

// --- Clear ---

func TestErrorStore_Clear(t *testing.T) {
	es := newTestErrorStore(t)
	es.LearnError("build", "error1", "solution1", nil)
	es.LearnError("test", "error2", "solution2", nil)

	if err := es.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if es.Count() != 0 {
		t.Errorf("Count after Clear = %d, want 0", es.Count())
	}
}

// --- PruneOldEntries ---

func TestErrorStore_PruneOldEntries(t *testing.T) {
	es := newTestErrorStore(t)

	// Insert entries directly (PruneOldEntries takes the lock itself)
	for i := range 10 {
		entry := &ErrorEntry{
			ID:          "err" + string(rune('A'+i)),
			ErrorType:   "build",
			Pattern:     "pattern" + string(rune('A'+i)),
			Solution:    "solution",
			SuccessRate: 0.2, // below 0.3 threshold → eligible for pruning
			UseCount:    i,
			LastUsed:    time.Now().Add(-time.Duration(10+i) * time.Hour), // old enough
			Created:     time.Now().Add(-time.Duration(i) * time.Hour),
		}
		es.mu.Lock()
		es.entries[entry.ID] = entry
		es.byType["build"] = append(es.byType["build"], entry.ID)
		es.mu.Unlock()
	}

	if err := es.PruneOldEntries(5 * time.Hour); err != nil {
		t.Fatalf("PruneOldEntries: %v", err)
	}

	count := es.Count()
	if count == 10 {
		t.Error("expected some entries to be pruned, but count is still 10")
	}
}

// --- GetLearnedErrors sorting ---

func TestErrorStore_GetLearnedErrors_SortedBySuccessRate(t *testing.T) {
	es := newTestErrorStore(t)
	es.mu.Lock()
	es.entries["high"] = &ErrorEntry{ID: "high", ErrorType: "build", Pattern: "shared", Solution: "high-sol", SuccessRate: 0.9, UseCount: 1}
	es.entries["low"] = &ErrorEntry{ID: "low", ErrorType: "build", Pattern: "shared", Solution: "low-sol", SuccessRate: 0.3, UseCount: 5}
	es.byType["build"] = []string{"high", "low"}
	es.mu.Unlock()

	matches := es.GetLearnedErrors("shared pattern here")
	if len(matches) < 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].ID != "high" {
		t.Errorf("first match should be 'high' (higher success rate), got %q", matches[0].ID)
	}
}

// --- load from existing file ---

func TestErrorStore_LoadFromExistingFile(t *testing.T) {
	dir := t.TempDir()
	es1, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("NewErrorStore: %v", err)
	}
	es1.LearnError("build", "persist this error", "persisted solution", nil)
	es1.Flush()

	// Create second store pointing at same dir
	es2, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("NewErrorStore (reload): %v", err)
	}
	t.Cleanup(func() { _ = es2.Flush() })

	matches := es2.GetLearnedErrors("persist this error")
	if len(matches) != 1 {
		t.Errorf("after reload, matches = %d, want 1", len(matches))
	}
	if matches[0].Solution != "persisted solution" {
		t.Errorf("Solution = %q, want 'persisted solution'", matches[0].Solution)
	}
}

// --- load with corrupt data ---

func TestErrorStore_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0755)
	os.WriteFile(filepath.Join(memDir, "errors.json"), []byte("not valid json"), 0644)

	es, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("NewErrorStore with corrupt file should not error: %v", err)
	}
	t.Cleanup(func() { _ = es.Flush() })
	if es.Count() != 0 {
		t.Error("corrupt file should result in empty store")
	}
}

// --- NewErrorStore MkdirAll failure ---

func TestNewErrorStore_MkdirFailure(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "iamfile")
	os.WriteFile(filePath, []byte("x"), 0644)

	_, err := NewErrorStore(filePath)
	if err == nil {
		t.Error("NewErrorStore should fail when MkdirAll fails")
	}
}

// --- Flush ---

func TestErrorStore_Flush_PersistsData(t *testing.T) {
	dir := t.TempDir()
	es, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("NewErrorStore: %v", err)
	}
	es.LearnError("build", "flush test error", "flush solution", nil)

	if err := es.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "memory", "errors.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "flush test error") {
		t.Error("flushed file should contain the error pattern")
	}
}

// --- Flush not dirty (no-op) ---

func TestErrorStore_Flush_NotDirtyNoOp(t *testing.T) {
	dir := t.TempDir()
	es, err := NewErrorStore(dir)
	if err != nil {
		t.Fatalf("NewErrorStore: %v", err)
	}
	// Nothing dirty
	if err := es.Flush(); err != nil {
		t.Errorf("Flush on clean store should return nil, got %v", err)
	}
}
