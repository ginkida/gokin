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

func TestToolResultCachePutSnapshotsArgsForInvalidation(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 10,
		TTL:        time.Minute,
	})

	args := map[string]any{"file_path": "a.txt"}
	cache.Put("read", args, NewSuccessResult("a-v1"))
	args["file_path"] = "b.txt"

	cache.InvalidateByFile("a.txt")

	if cache.GetStats().Entries != 0 {
		t.Fatalf("Entries = %d, want 0 after invalidating original file path", cache.GetStats().Entries)
	}
}

func TestToolResultCachePutDeepSnapshotsNestedArgs(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 10,
		TTL:        time.Minute,
	})

	args := map[string]any{
		"path":    "repo",
		"filters": []any{"*.go", map[string]any{"kind": "test"}},
	}
	cache.Put("tree", args, NewSuccessResult("snapshot"))

	args["filters"].([]any)[1].(map[string]any)["kind"] = "prod"

	cache.Invalidate("tree", map[string]any{
		"path":    "repo",
		"filters": []any{"*.go", map[string]any{"kind": "test"}},
	})

	if cache.GetStats().Entries != 0 {
		t.Fatalf("Entries = %d, want 0 after invalidating original nested args", cache.GetStats().Entries)
	}
}

func TestToolResultCacheKeyStableAcrossArgOrder(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 10,
		TTL:        time.Minute,
	})

	cache.Put("grep", map[string]any{
		"pattern":          "TODO",
		"path":             "internal",
		"case_insensitive": true,
	}, NewSuccessResult("matches"))

	result, ok := cache.Get("grep", map[string]any{
		"case_insensitive": true,
		"path":             "internal",
		"pattern":          "TODO",
	})
	if !ok {
		t.Fatal("cache key should be stable across argument map order")
	}
	if result.Content != "matches" {
		t.Fatalf("cached content = %q, want matches", result.Content)
	}
}

func TestToolResultCacheKeyDistinguishesValueTypes(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 10,
		TTL:        time.Minute,
	})

	cache.Put("read", map[string]any{"file_path": "x.go", "offset": 1}, NewSuccessResult("numeric"))
	cache.Put("read", map[string]any{"file_path": "x.go", "offset": "1"}, NewSuccessResult("string"))

	if cache.GetStats().Entries != 2 {
		t.Fatalf("Entries = %d, want 2 for int and string offsets", cache.GetStats().Entries)
	}

	result, ok := cache.Get("read", map[string]any{"file_path": "x.go", "offset": 1})
	if !ok || result.Content != "numeric" {
		t.Fatalf("numeric offset cache lookup = ok:%v content:%q, want numeric", ok, result.Content)
	}
	result, ok = cache.Get("read", map[string]any{"file_path": "x.go", "offset": "1"})
	if !ok || result.Content != "string" {
		t.Fatalf("string offset cache lookup = ok:%v content:%q, want string", ok, result.Content)
	}
}

func TestToolResultCacheKeyDistinguishesNumericTypes(t *testing.T) {
	cache := NewToolResultCache(CacheConfig{
		MaxEntries: 10,
		TTL:        time.Minute,
	})

	cache.Put("read", map[string]any{"file_path": "x.go", "offset": 1}, NewSuccessResult("int"))
	cache.Put("read", map[string]any{"file_path": "x.go", "offset": float64(1)}, NewSuccessResult("float"))

	if cache.GetStats().Entries != 2 {
		t.Fatalf("Entries = %d, want 2 for int and float64 offsets", cache.GetStats().Entries)
	}

	result, ok := cache.Get("read", map[string]any{"file_path": "x.go", "offset": 1})
	if !ok || result.Content != "int" {
		t.Fatalf("int offset cache lookup = ok:%v content:%q, want int", ok, result.Content)
	}
	result, ok = cache.Get("read", map[string]any{"file_path": "x.go", "offset": float64(1)})
	if !ok || result.Content != "float" {
		t.Fatalf("float offset cache lookup = ok:%v content:%q, want float", ok, result.Content)
	}
}
