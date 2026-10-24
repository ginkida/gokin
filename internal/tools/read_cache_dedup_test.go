package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/testkit"
)

func TestReadCachePreservesDedupAndSelfHealing(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprint(cached), func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			path := filepath.Join(root, "m.go")
			if err := os.WriteFile(path, []byte("package m\nfunc Add(a, b int) int { return a + b }\n"), 0600); err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry()
			registry.MustRegister(NewReadTool(root))
			executor := NewExecutor(registry, nil, time.Second)
			executor.SetReadTracker(NewFileReadTracker())
			if cached {
				executor.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
			}
			for read := range 4 {
				result, err := executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": path})
				stub := strings.HasPrefix(result.Content, "[Unchanged since turn ")
				if err != nil || !result.Success || stub != (read == 1 || read == 3) {
					t.Fatalf("read %d bypassed dedup/self-healing: %+v %v", read+1, result, err)
				}
			}
		})
	}
}
