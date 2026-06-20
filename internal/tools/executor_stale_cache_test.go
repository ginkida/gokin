package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/testkit"
)

// TestExecutor_ReadCacheBypassedWhenFileChangedOnDisk pins the stale-read-cache
// fix: a `read` cache hit must NOT be served when the file changed on disk since
// it was cached (e.g. an out-of-band bash/codegen write that carries no file_path
// arg, so the normal write-path invalidation never fired). CheckAndRecord already
// computes the fileChanged signal; the executor must honor it and serve fresh
// content instead of pre-write bytes.
func TestExecutor_ReadCacheBypassedWhenFileChangedOnDisk(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	path := filepath.Join(workDir, "code.go")
	if err := os.WriteFile(path, []byte("VERSION_ONE\n"), 0644); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry()
	if err := registry.Register(NewReadTool(workDir)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	exec := NewExecutor(registry, nil, time.Second)
	exec.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
	exec.SetReadTracker(NewFileReadTracker())

	call := &genai.FunctionCall{ID: "r1", Name: "read", Args: map[string]any{"file_path": path}}

	// First read: real execution, content cached.
	r1 := exec.doExecuteTool(context.Background(), call)
	if !r1.Success || !strings.Contains(r1.Content, "VERSION_ONE") {
		t.Fatalf("first read should return VERSION_ONE: success=%v content=%q", r1.Success, r1.Content)
	}

	// Out-of-band change (different size guarantees CheckAndRecord sees a change
	// even on coarse-mtime filesystems).
	if err := os.WriteFile(path, []byte("VERSION_TWO_is_longer\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Second read with identical args: a TTL-only cache would serve the stale
	// VERSION_ONE; the fix must detect the change and serve VERSION_TWO.
	call2 := &genai.FunctionCall{ID: "r2", Name: "read", Args: map[string]any{"file_path": path}}
	r2 := exec.doExecuteTool(context.Background(), call2)
	if !r2.Success {
		t.Fatalf("second read failed: %q", r2.Content)
	}
	if strings.Contains(r2.Content, "VERSION_ONE") || !strings.Contains(r2.Content, "VERSION_TWO") {
		t.Errorf("stale cache served pre-write content (the out-of-band-write bug):\n%s", r2.Content)
	}
}

// TestExecutor_ReadCacheServedWhenFileUnchanged pins that the bypass is narrow:
// an UNCHANGED file still serves from cache (the dedup/cache optimization is
// preserved — we only bypass on a real on-disk change).
func TestExecutor_ReadCacheServedWhenFileUnchanged(t *testing.T) {
	workDir := testkit.ResolvedTempDir(t)
	path := filepath.Join(workDir, "stable.go")
	if err := os.WriteFile(path, []byte("STABLE_CONTENT\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cache := NewToolResultCache(DefaultCacheConfig())
	registry := NewRegistry()
	if err := registry.Register(NewReadTool(workDir)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	exec := NewExecutor(registry, nil, time.Second)
	exec.SetToolCache(cache)
	exec.SetReadTracker(NewFileReadTracker())

	call := &genai.FunctionCall{ID: "r1", Name: "read", Args: map[string]any{"file_path": path}}
	exec.doExecuteTool(context.Background(), call)

	// The entry must still be cached (unchanged file → no invalidation).
	if _, hit := cache.Get("read", call.Args); !hit {
		t.Error("an unchanged file's read should remain cached; the bypass over-fired")
	}
}
