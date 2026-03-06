package memory

import (
	"testing"
	"time"
)

func TestNewEntry(t *testing.T) {
	e := NewEntry("test content", MemoryProject)
	if e.ID == "" {
		t.Error("ID should be generated")
	}
	if e.Content != "test content" {
		t.Errorf("Content = %q", e.Content)
	}
	if e.Type != MemoryProject {
		t.Errorf("Type = %q", e.Type)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}
}

func TestEntryWithKey(t *testing.T) {
	e := NewEntry("content", MemorySession).WithKey("my-key")
	if e.Key != "my-key" {
		t.Errorf("Key = %q", e.Key)
	}
}

func TestEntryWithTags(t *testing.T) {
	e := NewEntry("content", MemoryGlobal).WithTags([]string{"go", "testing"})
	if len(e.Tags) != 2 {
		t.Errorf("Tags = %v", e.Tags)
	}
}

func TestEntryWithProject(t *testing.T) {
	e := NewEntry("content", MemoryProject).WithProject("proj-hash")
	if e.Project != "proj-hash" {
		t.Errorf("Project = %q", e.Project)
	}
}

func TestEntryWithTTL(t *testing.T) {
	e := NewEntry("content", MemorySession).WithTTL(time.Hour)
	if e.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should be set")
	}
	if time.Until(e.ExpiresAt) < 59*time.Minute {
		t.Error("ExpiresAt should be ~1 hour from now")
	}

	// Zero TTL should not set expiry
	e2 := NewEntry("content", MemorySession).WithTTL(0)
	if !e2.ExpiresAt.IsZero() {
		t.Error("zero TTL should not set ExpiresAt")
	}
}

func TestEntryHasTag(t *testing.T) {
	e := NewEntry("content", MemoryProject).WithTags([]string{"Go", "Testing"})

	if !e.HasTag("go") {
		t.Error("should match case-insensitive")
	}
	if !e.HasTag("  Testing  ") {
		t.Error("should match with whitespace trimming")
	}
	if e.HasTag("python") {
		t.Error("should not match non-existent tag")
	}
}

func TestEntryIsExpired(t *testing.T) {
	now := time.Now()

	// No expiry
	e := NewEntry("content", MemorySession)
	if e.IsExpired(now) {
		t.Error("no expiry should not be expired")
	}

	// Expired
	e.ExpiresAt = now.Add(-time.Hour)
	if !e.IsExpired(now) {
		t.Error("past expiry should be expired")
	}

	// Not yet expired
	e.ExpiresAt = now.Add(time.Hour)
	if e.IsExpired(now) {
		t.Error("future expiry should not be expired")
	}
}

func TestEntrySuccessRate(t *testing.T) {
	e := NewEntry("content", MemorySession)

	// No data: Laplace smoothing default
	rate := e.SuccessRate()
	if rate != 0.5 {
		t.Errorf("no data rate = %f, want 0.5", rate)
	}

	// All success: (success+1)/(total+2) = (3+1)/(3+2) = 0.8
	e.SuccessCount = 3
	rate = e.SuccessRate()
	if rate != 0.8 {
		t.Errorf("3 success rate = %f, want 0.8", rate)
	}

	// Mixed: (2+1)/(5+2) = 3/7
	e.SuccessCount = 2
	e.FailureCount = 3
	rate = e.SuccessRate()
	expected := 3.0 / 7.0
	if rate < expected-0.001 || rate > expected+0.001 {
		t.Errorf("mixed rate = %f, want %f", rate, expected)
	}
}

func TestEntryRecordAccess(t *testing.T) {
	e := NewEntry("content", MemorySession)
	e.RecordAccess()
	if e.AccessCount != 1 {
		t.Errorf("AccessCount = %d", e.AccessCount)
	}
	if e.LastAccessed.IsZero() {
		t.Error("LastAccessed should be set")
	}
}

func TestEntryRecordOutcome(t *testing.T) {
	e := NewEntry("content", MemorySession)
	e.RecordOutcome(true)
	e.RecordOutcome(true)
	e.RecordOutcome(false)
	if e.SuccessCount != 2 {
		t.Errorf("SuccessCount = %d", e.SuccessCount)
	}
	if e.FailureCount != 1 {
		t.Errorf("FailureCount = %d", e.FailureCount)
	}
}

func TestEntryMatches(t *testing.T) {
	e := &Entry{
		Key:     "db-config",
		Content: "use PostgreSQL",
		Type:    MemoryProject,
		Project: "proj-1",
		Tags:    []string{"database", "config"},
	}

	// Empty query matches all
	if !e.Matches(SearchQuery{}) {
		t.Error("empty query should match")
	}

	// Key match
	if !e.Matches(SearchQuery{Key: "db-config"}) {
		t.Error("matching key should match")
	}
	if e.Matches(SearchQuery{Key: "other"}) {
		t.Error("different key should not match")
	}

	// Tag filter
	if !e.Matches(SearchQuery{Tags: []string{"database"}}) {
		t.Error("matching tag should match")
	}
	if e.Matches(SearchQuery{Tags: []string{"database", "python"}}) {
		t.Error("missing tag should not match")
	}

	// Project filter
	if !e.Matches(SearchQuery{ProjectOnly: true, Project: "proj-1"}) {
		t.Error("matching project should match")
	}
	if e.Matches(SearchQuery{ProjectOnly: true, Project: "proj-2"}) {
		t.Error("different project should not match")
	}

	// Archived filter
	e.Archived = true
	if e.Matches(SearchQuery{}) {
		t.Error("archived entry should not match without IncludeArchived")
	}
	if !e.Matches(SearchQuery{IncludeArchived: true}) {
		t.Error("archived entry should match with IncludeArchived")
	}
}

func TestGenerateID(t *testing.T) {
	id1 := generateID("content1")
	id2 := generateID("content2")
	if id1 == id2 {
		t.Error("different content should generate different IDs")
	}
	if len(id1) < 10 {
		t.Errorf("ID too short: %q", id1)
	}
	if id1[:4] != "mem_" {
		t.Errorf("ID should start with mem_: %q", id1)
	}
}

// --- Store helper tests ---

func TestExtractContentTags(t *testing.T) {
	// File paths
	tags := extractContentTags("Read /src/main.go for details")
	found := false
	for _, tag := range tags {
		if tag == "/src/main.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("should extract file path, got %v", tags)
	}

	// Function names
	tags = extractContentTags("func HandleRequest processes HTTP")
	found = false
	for _, tag := range tags {
		if tag == "HandleRequest" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("should extract function name, got %v", tags)
	}

	// Package names
	tags = extractContentTags("package main")
	found = false
	for _, tag := range tags {
		if tag == "main" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("should extract package name, got %v", tags)
	}

	// Empty
	tags = extractContentTags("")
	if len(tags) != 0 {
		t.Errorf("empty should return no tags, got %v", tags)
	}
}

func TestNormalizeMemoryText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "hello world"},
		{"  spaces  everywhere  ", "spaces everywhere"},
		{"path/to/file.go", "path/to/file.go"},
		{"special@#$chars", "special chars"},
		{"under_score-dash", "under_score-dash"},
	}

	for _, tt := range tests {
		got := normalizeMemoryText(tt.input)
		if got != tt.want {
			t.Errorf("normalizeMemoryText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestTokenSetFromText(t *testing.T) {
	set := tokenSetFromText("we use PostgreSQL with pgx driver")
	if _, ok := set["we"]; ok {
		t.Error("short words (<3 chars) should be excluded")
	}
	if _, ok := set["postgresql"]; !ok {
		t.Error("should include 'postgresql'")
	}
	if _, ok := set["driver"]; !ok {
		t.Error("should include 'driver'")
	}
}

func TestJaccardSimilarity(t *testing.T) {
	// Both empty
	if jaccardSimilarity(nil, nil) != 1 {
		t.Error("both empty should be 1")
	}

	a := map[string]struct{}{"hello": {}, "world": {}}
	b := map[string]struct{}{"hello": {}, "earth": {}}

	sim := jaccardSimilarity(a, b)
	// intersection=1 (hello), union=3 (hello,world,earth) => 1/3
	expected := 1.0 / 3.0
	if sim < expected-0.01 || sim > expected+0.01 {
		t.Errorf("sim = %f, want %f", sim, expected)
	}

	// Identical
	sim = jaccardSimilarity(a, a)
	if sim != 1.0 {
		t.Errorf("identical = %f, want 1.0", sim)
	}

	// Disjoint
	c := map[string]struct{}{"foo": {}, "bar": {}}
	sim = jaccardSimilarity(a, c)
	if sim != 0 {
		t.Errorf("disjoint = %f, want 0", sim)
	}
}

func TestMergeTags(t *testing.T) {
	// Basic merge
	result := mergeTags([]string{"go", "testing"}, []string{"rust", "testing"})
	if len(result) != 3 {
		t.Errorf("merge len = %d, want 3", len(result))
	}

	// Empty inputs
	result = mergeTags(nil, []string{"a"})
	if len(result) != 1 {
		t.Errorf("nil + 1 = %d", len(result))
	}

	// Case-insensitive dedup
	result = mergeTags([]string{"Go"}, []string{"go"})
	if len(result) != 1 {
		t.Errorf("case dedup = %d, want 1", len(result))
	}

	// Whitespace trimming
	result = mergeTags([]string{" tag "}, []string{"tag"})
	if len(result) != 1 {
		t.Errorf("whitespace dedup = %d, want 1", len(result))
	}
}

func TestAutoTag(t *testing.T) {
	e := &Entry{
		Content: "func HandleRequest in /src/handler.go",
		Tags:    []string{"existing"},
	}
	autoTag(e)
	if len(e.Tags) < 2 {
		t.Errorf("should have added tags, got %v", e.Tags)
	}
	// Should not duplicate existing tags
	has := false
	for _, tag := range e.Tags {
		if tag == "existing" {
			if has {
				t.Error("duplicate 'existing' tag")
			}
			has = true
		}
	}
}

func TestStoreAddAndGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, "/test/project", 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	e := NewEntry("PostgreSQL is our database", MemoryProject).WithKey("db")
	if err := store.Add(e); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, ok := store.Get("db")
	if !ok {
		t.Fatal("Get should find entry")
	}
	if got.Content != "PostgreSQL is our database" {
		t.Errorf("Content = %q", got.Content)
	}

	// Non-existent key
	_, ok = store.Get("nonexistent")
	if ok {
		t.Error("non-existent key should not be found")
	}
}

func TestStoreAddGlobal(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, "/test/project", 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	e := NewEntry("global fact", MemoryGlobal).WithKey("global-key")
	store.Add(e)

	got, ok := store.Get("global-key")
	if !ok {
		t.Fatal("should find global entry")
	}
	if got.Type != MemoryGlobal {
		t.Errorf("Type = %q, want global", got.Type)
	}
}

func TestStoreExpiredEntry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir, "/test/project", 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	e := NewEntry("temporary", MemorySession).WithKey("temp")
	e.ExpiresAt = time.Now().Add(-time.Hour) // Already expired
	store.Add(e)

	_, ok := store.Get("temp")
	if ok {
		t.Error("expired entry should not be returned by Get")
	}
}
