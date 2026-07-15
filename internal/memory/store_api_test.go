package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// newTestStore creates a Store in a temp dir with a small maxEntries cap.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir(), "/test/project", 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

// --- GetByID ---

func TestGetByID_ProjectEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("project fact", MemoryProject).WithKey("pk")
	store.Add(e)

	got, ok := store.GetByID(e.ID)
	if !ok {
		t.Fatal("GetByID should find project entry")
	}
	if got.Content != "project fact" {
		t.Errorf("Content = %q, want %q", got.Content, "project fact")
	}
}

func TestGetByID_GlobalEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("global fact", MemoryGlobal).WithKey("gk")
	store.Add(e)

	got, ok := store.GetByID(e.ID)
	if !ok {
		t.Fatal("GetByID should find global entry")
	}
	if got.Type != MemoryGlobal {
		t.Errorf("Type = %q, want global", got.Type)
	}
}

func TestGetByID_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, ok := store.GetByID("does-not-exist")
	if ok {
		t.Error("GetByID should return false for non-existent ID")
	}
}

func TestGetByID_ExpiredEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("temporary", MemorySession).WithKey("temp")
	e.ExpiresAt = time.Now().Add(-time.Hour)
	store.Add(e)

	_, ok := store.GetByID(e.ID)
	if ok {
		t.Error("GetByID should not return expired entry")
	}
}

// --- RecordAccess ---

func TestRecordAccess_ProjectEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("access me", MemoryProject).WithKey("ak")
	store.Add(e)

	before := e.AccessCount
	store.RecordAccess(e.ID)
	got, _ := store.GetByID(e.ID)
	if got.AccessCount != before+1 {
		t.Errorf("AccessCount = %d, want %d", got.AccessCount, before+1)
	}
}

func TestRecordAccess_GlobalEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("global access", MemoryGlobal).WithKey("gak")
	store.Add(e)

	before := e.AccessCount
	store.RecordAccess(e.ID)
	got, _ := store.GetByID(e.ID)
	if got.AccessCount != before+1 {
		t.Errorf("AccessCount = %d, want %d", got.AccessCount, before+1)
	}
}

func TestRecordAccess_NotFound(t *testing.T) {
	store := newTestStore(t)
	// Should not panic
	store.RecordAccess("nonexistent-id")
}

// --- RecordFeedback ---

func TestRecordFeedback_Success(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("feedback target", MemoryProject).WithKey("fk")
	store.Add(e)

	ok := store.RecordFeedback(e.ID, true)
	if !ok {
		t.Error("RecordFeedback should return true for existing entry")
	}
	got, _ := store.GetByID(e.ID)
	if got.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1", got.SuccessCount)
	}
}

func TestRecordFeedback_Failure(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("feedback target", MemoryProject).WithKey("fk2")
	store.Add(e)

	ok := store.RecordFeedback(e.ID, false)
	if !ok {
		t.Error("RecordFeedback should return true for existing entry")
	}
	got, _ := store.GetByID(e.ID)
	if got.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", got.FailureCount)
	}
}

func TestRecordFeedback_NotFound(t *testing.T) {
	store := newTestStore(t)
	ok := store.RecordFeedback("nonexistent", true)
	if ok {
		t.Error("RecordFeedback should return false for non-existent entry")
	}
}

// --- Edit ---

func TestEdit_ExistingEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("original content", MemoryProject).WithKey("ek")
	store.Add(e)

	if err := store.Edit(e.ID, "updated content"); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	got, ok := store.GetByID(e.ID)
	if !ok {
		t.Fatal("entry should still exist after edit")
	}
	if got.Content != "updated content" {
		t.Errorf("Content = %q, want %q", got.Content, "updated content")
	}
}

func TestEdit_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.Edit("nonexistent", "new content")
	if err == nil {
		t.Error("Edit should return error for non-existent entry")
	}
}

func TestEdit_RetagsContent(t *testing.T) {
	store := newTestStore(t)
	// autoTag extracts file paths starting with /
	e := NewEntry("see /old/path.go for details", MemoryProject).WithKey("tk")
	store.Add(e)

	hadOld := false
	got0, _ := store.GetByID(e.ID)
	for _, tag := range got0.Tags {
		if tag == "/old/path.go" {
			hadOld = true
		}
	}
	if !hadOld {
		t.Fatalf("setup: tags = %v, want /old/path.go present", got0.Tags)
	}

	// Edit to reference a different path
	store.Edit(e.ID, "see /new/path.go for details")

	got, _ := store.GetByID(e.ID)
	foundNew := false
	for _, tag := range got.Tags {
		if tag == "/new/path.go" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Errorf("tags after edit = %v, want '/new/path.go' present", got.Tags)
	}
}

// --- Remove ---

func TestRemove_ByKey(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("remove me", MemoryProject).WithKey("rk")
	store.Add(e)

	ok := store.Remove("rk")
	if !ok {
		t.Error("Remove by key should return true")
	}
	_, exists := store.Get("rk")
	if exists {
		t.Error("entry should be gone after Remove")
	}
}

func TestRemove_ByID(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("remove by id", MemoryProject).WithKey("rk2")
	store.Add(e)

	ok := store.Remove(e.ID)
	if !ok {
		t.Error("Remove by ID should return true")
	}
}

func TestRemove_GlobalEntry(t *testing.T) {
	store := newTestStore(t)
	e := NewEntry("global removal", MemoryGlobal).WithKey("grk")
	store.Add(e)

	ok := store.Remove(e.ID)
	if !ok {
		t.Error("Remove global should return true")
	}
}

func TestRemove_NotFound(t *testing.T) {
	store := newTestStore(t)
	ok := store.Remove("totally-fake")
	if ok {
		t.Error("Remove should return false for non-existent entry")
	}
}

// --- List / ListAll ---

func TestList_ProjectOnly(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("project entry", MemoryProject).WithKey("p1"))
	store.Add(NewEntry("global entry", MemoryGlobal).WithKey("g1"))

	results := store.List(true)
	for _, e := range results {
		if e.Type == MemoryGlobal {
			t.Error("List(true) should not include global entries")
		}
	}
}

func TestList_All(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("project entry", MemoryProject).WithKey("p2"))
	store.Add(NewEntry("global entry", MemoryGlobal).WithKey("g2"))

	results := store.List(false)
	if len(results) < 2 {
		t.Errorf("List(false) returned %d entries, want >= 2", len(results))
	}
}

func TestListAll_SortedByTimestamp(t *testing.T) {
	store := newTestStore(t)
	e1 := NewEntry("first", MemoryProject).WithKey("l1")
	e1.Timestamp = time.Now().Add(-2 * time.Hour)
	store.Add(e1)
	e2 := NewEntry("second", MemoryProject).WithKey("l2")
	e2.Timestamp = time.Now().Add(-1 * time.Hour)
	store.Add(e2)

	results := store.ListAll()
	if len(results) < 2 {
		t.Fatalf("ListAll returned %d, want >= 2", len(results))
	}
	// newest first
	if results[0].Timestamp.Before(results[1].Timestamp) {
		t.Error("ListAll should sort newest first")
	}
}

func TestListAll_SkipsExpired(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("alive", MemoryProject).WithKey("a1"))
	expired := NewEntry("dead", MemorySession).WithKey("d1")
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	store.Add(expired)

	for _, e := range store.ListAll() {
		if e.Key == "d1" {
			t.Error("ListAll should skip expired entries")
		}
	}
}

// --- Count ---

func TestCount(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("p", MemoryProject).WithKey("c1"))
	store.Add(NewEntry("g", MemoryGlobal).WithKey("c2"))

	if n := store.Count(); n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
}

// --- Clear ---

func TestClear(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("p", MemoryProject).WithKey("cl1"))
	store.Add(NewEntry("g", MemoryGlobal).WithKey("cl2"))

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n := store.Count(); n != 0 {
		t.Errorf("Count after Clear = %d, want 0", n)
	}
}

// --- GetReport ---

func TestGetReport_Empty(t *testing.T) {
	store := newTestStore(t)
	report := store.GetReport()
	if !strings.Contains(report, "No memories") {
		t.Errorf("empty report = %q, want 'No memories'", report)
	}
}

func TestGetReport_WithEntries(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("project detail here", MemoryProject).WithKey("rpt1"))
	store.Add(NewEntry("global pref", MemoryGlobal).WithKey("rpt2"))

	report := store.GetReport()
	if !strings.Contains(report, "Stored Memories") {
		t.Errorf("report missing header: %q", report)
	}
	if !strings.Contains(report, "Project") {
		t.Errorf("report missing Project group: %q", report)
	}
	if !strings.Contains(report, "Global") {
		t.Errorf("report missing Global group: %q", report)
	}
}

// --- formatAge ---

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{48 * time.Hour, "2d ago"},
	}
	for _, tc := range tests {
		got := formatAge(tc.d)
		if got != tc.want {
			t.Errorf("formatAge(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// --- Export / Import ---

func TestExportImport_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("exportable project fact", MemoryProject).WithKey("ex1"))
	store.Add(NewEntry("exportable global fact", MemoryGlobal).WithKey("ex2"))

	data, err := store.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import into a fresh store
	store2 := newTestStore(t)
	if err := store2.Import(data); err != nil {
		t.Fatalf("Import: %v", err)
	}

	got, ok := store2.Get("ex1")
	if !ok {
		t.Error("imported project entry not found")
	}
	if got.Content != "exportable project fact" {
		t.Errorf("imported content = %q", got.Content)
	}

	got2, ok := store2.Get("ex2")
	if !ok {
		t.Error("imported global entry not found")
	}
	if got2.Type != MemoryGlobal {
		t.Errorf("imported type = %q, want global", got2.Type)
	}
}

func TestImport_SkipsDuplicates(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("original", MemoryProject).WithKey("dup1"))

	// Export and re-import into same store
	data, _ := store.Export()
	if err := store.Import(data); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Should not have duplicated
	if n := store.Count(); n != 1 {
		t.Errorf("Count after duplicate import = %d, want 1", n)
	}
}

func TestImport_BadJSON(t *testing.T) {
	store := newTestStore(t)
	err := store.Import([]byte("not valid json {{{"))
	if err == nil {
		t.Error("Import should fail on invalid JSON")
	}
}

func TestExport_EmptyStore(t *testing.T) {
	store := newTestStore(t)
	data, err := store.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var entries []*Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("empty export has %d entries, want 0", len(entries))
	}
}

// --- GetForContext ---

func TestGetForContext_Empty(t *testing.T) {
	store := newTestStore(t)
	result := store.GetForContext(false)
	if result != "" {
		t.Errorf("empty context = %q, want empty string", result)
	}
}

func TestGetForContext_WithEntries(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("session note", MemorySession).WithKey("ctx1"))
	store.Add(NewEntry("project knowledge", MemoryProject).WithKey("ctx2"))

	result := store.GetForContext(false)
	if !strings.Contains(result, "## Memory") {
		t.Errorf("context missing header: %q", result)
	}
	if !strings.Contains(result, "session note") {
		t.Errorf("context missing session content: %q", result)
	}
}

func TestGetForContext_Caching(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("cached entry", MemoryProject).WithKey("cache1"))

	first := store.GetForContext(false)
	// Second call should return the cached result
	second := store.GetForContext(false)
	if first != second {
		t.Error("cached result should be identical on second call")
	}

	// After mutation, cache should be invalidated
	store.Add(NewEntry("new entry", MemoryProject).WithKey("cache2"))
	third := store.GetForContext(false)
	if third == first {
		t.Error("cache should be invalidated after mutation")
	}
}

func TestGetForContext_ProjectOnly(t *testing.T) {
	store := newTestStore(t)
	store.Add(NewEntry("project only", MemoryProject).WithKey("po1"))
	store.Add(NewEntry("global too", MemoryGlobal).WithKey("po2"))

	result := store.GetForContext(true)
	if !strings.Contains(result, "project only") {
		t.Errorf("project-only context missing project entry: %q", result)
	}
}
