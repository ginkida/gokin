package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- SearchCache: grep/glob round-trip ---

func TestSearchCache_GrepRoundTrip(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	key := GrepKey("foo", "/src", "*.go", false, 3)
	result := GrepResult{
		Matches: []GrepMatch{
			{FilePath: "/src/a.go", LineNum: 10, Line: "foo()"},
			{FilePath: "/src/b.go", LineNum: 20, Line: "barfoo()"},
		},
		MatchCount: 2,
		FileCount:  2,
	}
	c.SetGrep(key, result)

	got, ok := c.GetGrep(key)
	if !ok {
		t.Fatal("GetGrep: expected cache hit")
	}
	if len(got.Matches) != 2 {
		t.Errorf("Matches len = %d, want 2", len(got.Matches))
	}
	if got.MatchCount != 2 {
		t.Errorf("MatchCount = %d, want 2", got.MatchCount)
	}
}

func TestSearchCache_GrepMiss(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	if _, ok := c.GetGrep("nonexistent"); ok {
		t.Error("expected miss for nonexistent key")
	}
}

func TestSearchCache_GlobRoundTrip(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	key := GlobKey("**/*.go", "/src")
	result := GlobResult{
		Files: []string{"/src/a.go", "/src/b.go"},
	}
	c.SetGlob(key, result)

	got, ok := c.GetGlob(key)
	if !ok {
		t.Fatal("GetGlob: expected cache hit")
	}
	if len(got.Files) != 2 {
		t.Errorf("Files len = %d, want 2", len(got.Files))
	}
}

func TestSearchCache_GlobMiss(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	if _, ok := c.GetGlob("nonexistent"); ok {
		t.Error("expected miss for nonexistent key")
	}
}

// --- SearchCache: disabled cache ---

func TestSearchCache_DisabledNoGetNoSet(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	c.SetEnabled(false)

	if c.IsEnabled() {
		t.Error("expected cache disabled")
	}

	key := GrepKey("foo", "/src", "", false, 0)
	c.SetGrep(key, GrepResult{Matches: []GrepMatch{{FilePath: "/x.go"}}})

	if _, ok := c.GetGrep(key); ok {
		t.Error("disabled cache should not return grep results")
	}

	gkey := GlobKey("*", "/src")
	c.SetGlob(gkey, GlobResult{Files: []string{"/x"}})
	if _, ok := c.GetGlob(gkey); ok {
		t.Error("disabled cache should not return glob results")
	}

	c.SetEnabled(true)
	if !c.IsEnabled() {
		t.Error("expected cache re-enabled")
	}
}

// --- SearchCache: InvalidateByPath ---

func TestSearchCache_InvalidateByPath(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	grepKey := GrepKey("foo", "/src", "*.go", false, 0)
	c.SetGrep(grepKey, GrepResult{
		Matches: []GrepMatch{{FilePath: "/src/a.go", LineNum: 1, Line: "foo"}},
	})

	globKey := GlobKey("*.go", "/src")
	c.SetGlob(globKey, GlobResult{Files: []string{"/src/a.go"}})

	// Invalidate by the file path that was tracked
	c.InvalidateByPath("/src/a.go")

	if _, ok := c.GetGrep(grepKey); ok {
		t.Error("grep entry should be invalidated")
	}
	if _, ok := c.GetGlob(globKey); ok {
		t.Error("glob entry should be invalidated")
	}
}

func TestSearchCache_InvalidateByPath_NoMatch(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	grepKey := GrepKey("foo", "/src", "*.go", false, 0)
	c.SetGrep(grepKey, GrepResult{
		Matches: []GrepMatch{{FilePath: "/src/a.go"}},
	})

	// Invalidate a different path — should not affect the entry
	c.InvalidateByPath("/src/other.go")

	if _, ok := c.GetGrep(grepKey); !ok {
		t.Error("grep entry should still exist after unrelated invalidation")
	}
}

func TestSearchCache_InvalidateByPath_Disabled(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	gk := GrepKey("foo", "/src", "", false, 0)
	c.SetGrep(gk, GrepResult{Matches: []GrepMatch{{FilePath: "/src/a.go"}}})

	c.SetEnabled(false)
	c.InvalidateByPath("/src/a.go")

	// Re-enable: entry should still be there because InvalidateByPath was a no-op when disabled
	c.SetEnabled(true)
	if _, ok := c.GetGrep(gk); !ok {
		t.Error("entry should still exist — InvalidateByPath was skipped while disabled")
	}
}

// --- SearchCache: InvalidateByDir ---

func TestSearchCache_InvalidateByDir(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	grepKey := GrepKey("foo", "/src", "*.go", false, 0)
	c.SetGrep(grepKey, GrepResult{
		Matches: []GrepMatch{
			{FilePath: "/src/sub/a.go"},
			{FilePath: "/src/sub/b.go"},
		},
	})

	c.InvalidateByDir("/src/sub")

	if _, ok := c.GetGrep(grepKey); ok {
		t.Error("grep entry should be invalidated by dir")
	}
}

func TestSearchCache_InvalidateByDir_NoMatch(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	grepKey := GrepKey("foo", "/src", "*.go", false, 0)
	c.SetGrep(grepKey, GrepResult{
		Matches: []GrepMatch{{FilePath: "/other/a.go"}},
	})

	c.InvalidateByDir("/src")

	if _, ok := c.GetGrep(grepKey); !ok {
		t.Error("grep entry should still exist (different dir)")
	}
}

func TestSearchCache_InvalidateByDir_Disabled(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	gk := GrepKey("foo", "/src", "", false, 0)
	c.SetGrep(gk, GrepResult{Matches: []GrepMatch{{FilePath: "/src/sub/a.go"}}})

	c.SetEnabled(false)
	c.InvalidateByDir("/src/sub")

	c.SetEnabled(true)
	if _, ok := c.GetGrep(gk); !ok {
		t.Error("entry should still exist — InvalidateByDir was skipped while disabled")
	}
}

// --- SearchCache: Clear ---

func TestSearchCache_Clear(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	gk := GrepKey("a", "/s", "", false, 0)
	glk := GlobKey("a", "/s")
	c.SetGrep(gk, GrepResult{Matches: []GrepMatch{{FilePath: "/s/a.go"}}})
	c.SetGlob(glk, GlobResult{Files: []string{"/s/a.go"}})

	c.Clear()

	stats := c.Stats()
	if stats.GrepEntries != 0 || stats.GlobEntries != 0 {
		t.Errorf("after Clear: GrepEntries=%d, GlobEntries=%d, want 0/0",
			stats.GrepEntries, stats.GlobEntries)
	}
}

// --- SearchCache: Cleanup ---

func TestSearchCache_CleanupRemovesExpired(t *testing.T) {
	// Use a short TTL; after expiry the entry should not be retrievable,
	// regardless of whether the background goroutine or explicit Cleanup got it.
	c := NewSearchCache(10, 30*time.Millisecond)
	defer c.StopCleanup()

	gk := GrepKey("a", "/s", "", false, 0)
	c.SetGrep(gk, GrepResult{Matches: []GrepMatch{{}}})

	time.Sleep(80 * time.Millisecond)

	// Entry should be expired — either background or explicit Cleanup removes it
	c.Cleanup()
	if _, ok := c.GetGrep(gk); ok {
		t.Error("expired grep entry should not be retrievable")
	}
}

func TestSearchCache_CleanupReturnsRemovedCount(t *testing.T) {
	// Use a long TTL so entries DON'T expire — Cleanup should return 0.
	c := NewSearchCache(10, time.Hour)
	defer c.StopCleanup()

	c.SetGrep(GrepKey("a", "/s", "", false, 0), GrepResult{Matches: []GrepMatch{{}}})
	c.SetGlob(GlobKey("a", "/s"), GlobResult{Files: []string{}})

	removed := c.Cleanup()
	if removed != 0 {
		t.Errorf("Cleanup with non-expired entries should return 0, got %d", removed)
	}
}

// --- SearchCache: Stats ---

func TestSearchCache_Stats(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	c.SetGrep(GrepKey("a", "/s", "", false, 0), GrepResult{Matches: []GrepMatch{{}}})
	c.SetGlob(GlobKey("a", "/s"), GlobResult{Files: []string{}})

	stats := c.Stats()
	if stats.GrepEntries != 1 {
		t.Errorf("GrepEntries = %d, want 1", stats.GrepEntries)
	}
	if stats.GlobEntries != 1 {
		t.Errorf("GlobEntries = %d, want 1", stats.GlobEntries)
	}
	if !stats.Enabled {
		t.Error("Enabled should be true")
	}
}

// --- SearchCache: StopCleanup idempotent ---

func TestSearchCache_StopCleanupIdempotent(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	c.StopCleanup()
	c.StopCleanup() // should not panic (double-close guard)

	// Verify cleanupDone is closed (non-blocking read)
	select {
	case <-c.cleanupDone:
		// good — channel is closed
	default:
		t.Error("cleanupDone should be closed after StopCleanup")
	}
}

// --- SearchCache: GrepKey / GlobKey determinism ---

func TestGrepKey_Deterministic(t *testing.T) {
	k1 := GrepKey("foo", "/src", "*.go", true, 3)
	k2 := GrepKey("foo", "/src", "*.go", true, 3)
	if k1 != k2 {
		t.Error("same args should produce same key")
	}
}

func TestGrepKey_DifferentArgs(t *testing.T) {
	k1 := GrepKey("foo", "/src", "*.go", true, 3)
	k2 := GrepKey("foo", "/src", "*.go", true, 4)
	if k1 == k2 {
		t.Error("different contextLines should produce different keys")
	}
}

func TestGlobKey_Deterministic(t *testing.T) {
	k1 := GlobKey("**/*.go", "/src")
	k2 := GlobKey("**/*.go", "/src")
	if k1 != k2 {
		t.Error("same args should produce same key")
	}
}

func TestGlobKey_DifferentArgs(t *testing.T) {
	k1 := GlobKey("**/*.go", "/src")
	k2 := GlobKey("**/*.ts", "/src")
	if k1 == k2 {
		t.Error("different patterns should produce different keys")
	}
}

// --- SearchCache: trackFiles dedup (same file in multiple matches) ---

func TestSearchCache_SetGrepDedupFiles(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	gk := GrepKey("foo", "/src", "", false, 0)
	c.SetGrep(gk, GrepResult{
		Matches: []GrepMatch{
			{FilePath: "/src/a.go", LineNum: 1},
			{FilePath: "/src/a.go", LineNum: 2}, // same file, different line
			{FilePath: "/src/b.go", LineNum: 3},
		},
	})

	// Invalidating /src/a.go should remove the entry (tracked once despite dup)
	c.InvalidateByPath("/src/a.go")

	if _, ok := c.GetGrep(gk); ok {
		t.Error("entry should be invalidated after /src/a.go changed")
	}
}

// --- SearchCache: overwrite grep accumulates file index (trackFiles is additive) ---

func TestSearchCache_SetGrepOverwrite(t *testing.T) {
	c := NewSearchCache(10, time.Minute)
	defer c.StopCleanup()

	gk := GrepKey("foo", "/src", "", false, 0)
	c.SetGrep(gk, GrepResult{
		Matches: []GrepMatch{{FilePath: "/src/a.go"}},
	})
	// Overwrite with different file
	c.SetGrep(gk, GrepResult{
		Matches: []GrepMatch{{FilePath: "/src/b.go"}},
	})

	// trackFiles is additive — both /src/a.go and /src/b.go are now tracked.
	// Invalidating old file removes the entry (both paths are indexed).
	c.InvalidateByPath("/src/a.go")
	if _, ok := c.GetGrep(gk); ok {
		t.Error("entry should be invalidated — trackFiles is additive, /src/a.go still tracked")
	}
}

// --- FormatCachedGrep ---

func TestFormatCachedGrep(t *testing.T) {
	result := GrepResult{
		Matches: []GrepMatch{
			{FilePath: "/src/sub/a.go", LineNum: 10, Line: "foo()"},
			{FilePath: "/src/b.go", LineNum: 20, Line: "bar()"},
		},
	}
	out := FormatCachedGrep(result, "/src")
	if !strings.Contains(out, "sub/a.go:10: foo()") {
		t.Errorf("expected relative path output, got: %s", out)
	}
	if !strings.Contains(out, "b.go:20: bar()") {
		t.Errorf("expected relative path output, got: %s", out)
	}
}

func TestFormatCachedGrep_RelErrorFallback(t *testing.T) {
	result := GrepResult{
		Matches: []GrepMatch{
			{FilePath: "/absolute/path.go", LineNum: 1, Line: "x"},
		},
	}
	// workDir that makes filepath.Rel fail (different volume on some systems,
	// but on unix this still resolves — so just verify no panic and non-empty)
	out := FormatCachedGrep(result, "/totally/different/root")
	if !strings.Contains(out, "path.go") {
		t.Errorf("expected path in output, got: %s", out)
	}
}

func TestFormatCachedGrep_Empty(t *testing.T) {
	out := FormatCachedGrep(GrepResult{}, "/src")
	if out != "" {
		t.Errorf("empty result should produce empty string, got: %s", out)
	}
}

// --- FileMetadataCache ---

func TestFileMetadataCache_SetGet(t *testing.T) {
	c := NewFileMetadataCache(10, time.Minute)

	c.Set("/some/file.go", &FileInfo{Exists: true, Size: 100})
	info, ok := c.Get("/some/file.go")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !info.Exists || info.Size != 100 {
		t.Errorf("got Exists=%v Size=%d", info.Exists, info.Size)
	}
}

func TestFileMetadataCache_GetMiss(t *testing.T) {
	c := NewFileMetadataCache(10, time.Minute)
	if _, ok := c.Get("/nonexistent"); ok {
		t.Error("expected miss")
	}
}

func TestFileMetadataCache_GetExpired(t *testing.T) {
	c := NewFileMetadataCache(10, 50*time.Millisecond)

	c.Set("/file.go", &FileInfo{Exists: true})
	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get("/file.go"); ok {
		t.Error("expired entry should not be returned")
	}
}

func TestFileMetadataCache_GetFileMetadata_RealFile(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "test.go")
	if err := os.WriteFile(fpath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	c := NewFileMetadataCache(10, time.Minute)
	info, err := c.GetFileMetadata(fpath)
	if err != nil {
		t.Fatalf("GetFileMetadata: %v", err)
	}
	if !info.Exists {
		t.Error("Exists should be true for real file")
	}
	if info.Size != 5 {
		t.Errorf("Size = %d, want 5", info.Size)
	}
	if info.IsDir {
		t.Error("IsDir should be false for file")
	}

	// Second call should hit cache
	info2, err := c.GetFileMetadata(fpath)
	if err != nil {
		t.Fatalf("GetFileMetadata (cached): %v", err)
	}
	if !info2.Exists || info2.Size != 5 {
		t.Errorf("cached info mismatch: %+v", info2)
	}
}

func TestFileMetadataCache_GetFileMetadata_NonExistent(t *testing.T) {
	c := NewFileMetadataCache(10, time.Minute)
	info, err := c.GetFileMetadata("/this/does/not/exist")
	if err != nil {
		t.Fatalf("GetFileMetadata non-existent: %v", err)
	}
	if info.Exists {
		t.Error("Exists should be false for non-existent file")
	}
}

func TestFileMetadataCache_GetFileMetadata_Directory(t *testing.T) {
	dir := t.TempDir()
	c := NewFileMetadataCache(10, time.Minute)
	info, err := c.GetFileMetadata(dir)
	if err != nil {
		t.Fatalf("GetFileMetadata dir: %v", err)
	}
	if !info.Exists {
		t.Error("Exists should be true for dir")
	}
	if !info.IsDir {
		t.Error("IsDir should be true for dir")
	}
}

func TestFileMetadataCache_Invalidate(t *testing.T) {
	c := NewFileMetadataCache(10, time.Minute)
	c.Set("/file.go", &FileInfo{Exists: true})

	c.Invalidate("/file.go")

	if _, ok := c.Get("/file.go"); ok {
		t.Error("entry should be invalidated")
	}
}

func TestFileMetadataCache_InvalidateByPrefix(t *testing.T) {
	c := NewFileMetadataCache(10, time.Minute)
	c.Set("/src/a.go", &FileInfo{Exists: true})
	c.Set("/src/b.go", &FileInfo{Exists: true})
	c.Set("/other/c.go", &FileInfo{Exists: true})

	c.InvalidateByPrefix("/src/")

	if _, ok := c.Get("/src/a.go"); ok {
		t.Error("/src/a.go should be invalidated")
	}
	if _, ok := c.Get("/src/b.go"); ok {
		t.Error("/src/b.go should be invalidated")
	}
	if _, ok := c.Get("/other/c.go"); !ok {
		t.Error("/other/c.go should still exist")
	}
}

// --- DirectoryTreeCache ---

func TestDirectoryTreeCache_SetGet(t *testing.T) {
	c := NewDirectoryTreeCache(10, time.Minute)

	entry := &TreeEntry{Path: "/root", Children: []string{"/root/a", "/root/b"}}
	c.Set("/root", entry)

	got, ok := c.Get("/root")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got.Children) != 2 {
		t.Errorf("Children len = %d, want 2", len(got.Children))
	}
}

func TestDirectoryTreeCache_GetMiss(t *testing.T) {
	c := NewDirectoryTreeCache(10, time.Minute)
	if _, ok := c.Get("/nonexistent"); ok {
		t.Error("expected miss")
	}
}

func TestDirectoryTreeCache_GetExpired(t *testing.T) {
	c := NewDirectoryTreeCache(10, 50*time.Millisecond)
	c.Set("/root", &TreeEntry{Path: "/root"})
	time.Sleep(100 * time.Millisecond)

	if _, ok := c.Get("/root"); ok {
		t.Error("expired entry should not be returned")
	}
}

func TestDirectoryTreeCache_BuildTree(t *testing.T) {
	dir := t.TempDir()
	// Create structure: dir/sub/file.go
	subDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file.go"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "top.go"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}

	c := NewDirectoryTreeCache(10, time.Minute)
	entry, err := c.BuildTree(dir, 3)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	if !entry.IsDir {
		t.Error("root entry should be a dir")
	}
	if len(entry.Children) < 2 {
		t.Errorf("expected at least 2 children, got %d", len(entry.Children))
	}

	// Second call should hit cache
	entry2, err := c.BuildTree(dir, 3)
	if err != nil {
		t.Fatalf("BuildTree (cached): %v", err)
	}
	if len(entry2.Children) != len(entry.Children) {
		t.Error("cached entry should match")
	}
}

func TestDirectoryTreeCache_BuildTree_NonExistent(t *testing.T) {
	c := NewDirectoryTreeCache(10, time.Minute)
	_, err := c.BuildTree("/this/does/not/exist", 3)
	if err == nil {
		t.Error("expected error for non-existent dir")
	}
}

func TestDirectoryTreeCache_BuildTree_MaxDepth(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	c := NewDirectoryTreeCache(10, time.Minute)
	// maxDepth=0: just returns the root entry without reading children
	entry, err := c.BuildTree(dir, 0)
	if err != nil {
		t.Fatalf("BuildTree maxDepth=0: %v", err)
	}
	if !entry.IsDir {
		t.Error("should be a dir")
	}
	// At depth 0, children list is empty (buildTreeRecursive returns early)
	if len(entry.Children) != 0 {
		t.Errorf("maxDepth=0 should have no children, got %d", len(entry.Children))
	}
}

func TestDirectoryTreeCache_Invalidate(t *testing.T) {
	c := NewDirectoryTreeCache(10, time.Minute)
	c.Set("/root", &TreeEntry{Path: "/root"})

	c.Invalidate("/root")

	if _, ok := c.Get("/root"); ok {
		t.Error("entry should be invalidated")
	}
}
