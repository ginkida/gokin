package tools

import (
	"testing"
	"time"
)

func TestToolResultCachePutExistingDoesNotEvictOtherEntries(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 2,
		TTL:        time.Minute,
	})

	cache.Put("read", map[string]any{"file_path": "a.txt"}, NewSuccessResult("a-v1"))
	cache.Put("read", map[string]any{"file_path": "b.txt"}, NewSuccessResult("b-v1"))
	cache.Put("read", map[string]any{"file_path": "b.txt"}, NewSuccessResult("b-v2"))

	if cache.GetStats().Entries != 2 {
		t.Fatalf("Entries = %d, want 2", cache.GetStats().Entries)
	}

	if result, ok := cache.Get("read", map[string]any{"file_path": "a.txt"}); !ok || result.Content != "a-v1" {
		t.Fatalf("entry a.txt missing or wrong after updating b.txt: ok=%v content=%q", ok, result.Content)
	}

	if result, ok := cache.Get("read", map[string]any{"file_path": "b.txt"}); !ok || result.Content != "b-v2" {
		t.Fatalf("entry b.txt not updated correctly: ok=%v content=%q", ok, result.Content)
	}
}

func TestToolResultCacheGetRemovesExpiredEntry(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 1,
		TTL:        10 * time.Millisecond,
	})

	args := map[string]any{"file_path": "expired.txt"}
	cache.Put("read", args, NewSuccessResult("stale"))
	time.Sleep(20 * time.Millisecond)

	if _, ok := cache.Get("read", args); ok {
		t.Fatal("expired entry should not be returned")
	}

	if cache.GetStats().Entries != 0 {
		t.Fatalf("Entries = %d, want 0 after expired read", cache.GetStats().Entries)
	}
}
