package cache

import (
	"testing"
	"time"
)

// --- maybeCleanup (42.9% → 100%) ---
// maybeCleanup runs when now.Sub(lastCleanup) >= cleanupInterval (5m).
// We can trigger it by backdating lastCleanup.

func TestMaybeCleanup_RemovesExpiredEntries(t *testing.T) {
	c := NewLRUCache[string, int](10, 50*time.Millisecond)
	c.Set("a", 1)
	c.Set("b", 2)

	// Backdate lastCleanup so maybeCleanup runs on next Set
	c.mu.Lock()
	c.lastCleanup = time.Now().Add(-10 * time.Minute)
	c.mu.Unlock()

	// Wait for entries to expire
	time.Sleep(100 * time.Millisecond)

	// Trigger maybeCleanup via Set
	c.Set("c", 3)

	// Expired entries should be gone
	if _, ok := c.Get("a"); ok {
		t.Fatal("expired entry 'a' should have been cleaned up")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("expired entry 'b' should have been cleaned up")
	}
	// New entry should survive
	if _, ok := c.Get("c"); !ok {
		t.Fatal("entry 'c' should still be in cache")
	}
}

func TestMaybeCleanup_SkipsWhenTooSoon(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	c.Set("a", 1)

	// lastCleanup is fresh (just set by NewLRUCache), so maybeCleanup should skip
	c.Set("b", 2)

	// Both entries should still be present
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (cleanup should have been skipped)", c.Len())
	}
}

// --- evictOldest with empty list (80% → 100%) ---

func TestEvictOldest_EmptyList(t *testing.T) {
	c := NewLRUCache[string, int](10, time.Hour)
	before := c.Len()
	// Directly call evictOldest on empty cache — should not panic
	c.mu.Lock()
	c.evictOldest()
	c.mu.Unlock()
	if got := c.Len(); got != before {
		t.Fatalf("Len after evictOldest on empty = %d, want %d", got, before)
	}
}

func TestEvictOldest_WithOnRemove(t *testing.T) {
	c := NewLRUCache[string, int](2, time.Hour)
	var removedKey string
	var removedVal int
	c.SetOnRemove(func(key string, val int) {
		removedKey = key
		removedVal = val
	})

	c.Set("a", 1)
	c.Set("b", 2)
	// Adding "c" should evict "a" (oldest) and fire onRemove
	c.Set("c", 3)

	if removedKey != "a" || removedVal != 1 {
		t.Fatalf("onRemove called with (%q, %d), want (%q, %d)", removedKey, removedVal, "a", 1)
	}
}

// --- FormatCachedGrep (85.7% → 100%) ---

func TestFormatCachedGrep_WithMatches(t *testing.T) {
	result := GrepResult{
		Matches: []GrepMatch{
			{FilePath: "main.go", LineNum: 10, Line: "func main() {}"},
			{FilePath: "util.go", LineNum: 5, Line: "func helper() {}"},
		},
		MatchCount: 2,
		FileCount:  2,
	}
	got := FormatCachedGrep(result, "/repo")
	if got == "" {
		t.Fatal("expected non-empty formatted output")
	}
}

func TestFormatCachedGrep_Truncation(t *testing.T) {
	matches := make([]GrepMatch, 20)
	for i := range matches {
		matches[i] = GrepMatch{FilePath: "file.go", LineNum: i + 1, Line: "line"}
	}
	result := GrepResult{Matches: matches, MatchCount: 20, FileCount: 1}
	got := FormatCachedGrep(result, "/repo")
	if got == "" {
		t.Fatal("expected non-empty formatted output")
	}
}
