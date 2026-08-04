package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gokin/internal/pinned"
)

func TestPinContextPersistenceFailureDoesNotUpdatePrompt(t *testing.T) {
	workDir := t.TempDir()
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(workDir, ".gokin")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	updated := "unchanged"
	tool := NewPinContextTool(func(content string) { updated = content })
	tool.SetWorkDir(workDir)

	result, err := tool.Execute(context.Background(), map[string]any{"content": "new pin"})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success || result.Error == "" {
		t.Fatalf("Execute result = %+v, want reported persistence failure", result)
	}
	if updated != "unchanged" {
		t.Fatalf("updater received %q despite persistence failure", updated)
	}
}

func TestPinContextRejectsOversizedContentBeforeUpdate(t *testing.T) {
	updated := false
	tool := NewPinContextTool(func(string) { updated = true })
	content := strings.Repeat("x", pinned.MaxContentBytes+1)
	if err := tool.Validate(map[string]any{"content": content}); err == nil {
		t.Fatal("Validate unexpectedly accepted oversized content")
	}
	result, err := tool.Execute(context.Background(), map[string]any{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || updated {
		t.Fatalf("oversized Execute result=%+v updated=%v", result, updated)
	}
}

func TestPinContextCloneCarriesWorkspace(t *testing.T) {
	workDir := t.TempDir()
	original := NewPinContextTool(nil)
	original.SetWorkDir(workDir)
	cloned, ok := CloneToolForWorkDir(original, "").(*PinContextTool)
	if !ok {
		t.Fatalf("clone type = %T", CloneToolForWorkDir(original, ""))
	}
	updated := ""
	cloned.SetUpdater(func(content string) { updated = content })
	result, err := cloned.Execute(context.Background(), map[string]any{"content": "carried"})
	if err != nil || !result.Success {
		t.Fatalf("Execute = %+v, %v", result, err)
	}
	if updated != "carried" {
		t.Fatalf("updated = %q", updated)
	}
	got, err := pinned.Load(workDir)
	if err != nil || got != "carried" {
		t.Fatalf("persisted pin = %q, %v", got, err)
	}
}

func TestPinContextConcurrentUpdatesKeepPromptAndDiskConsistent(t *testing.T) {
	workDir := t.TempDir()
	var (
		updatedMu sync.Mutex
		updated   string
	)
	tool := NewPinContextTool(func(content string) {
		updatedMu.Lock()
		updated = content
		updatedMu.Unlock()
	})
	tool.SetWorkDir(workDir)

	const writers = 32
	start := make(chan struct{})
	results := make(chan ToolResult, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			result, err := tool.Execute(context.Background(), map[string]any{
				"content": fmt.Sprintf("pin-%02d", i),
			})
			if err != nil {
				results <- NewErrorResult(err.Error())
				return
			}
			results <- result
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if !result.Success {
			t.Fatalf("concurrent Execute failed: %+v", result)
		}
	}

	persisted, err := pinned.Load(workDir)
	if err != nil {
		t.Fatal(err)
	}
	updatedMu.Lock()
	active := updated
	updatedMu.Unlock()
	if persisted != active {
		t.Fatalf("persisted pin %q differs from active prompt pin %q", persisted, active)
	}
}

func TestLoadPersistedPinAppliesDurableClearMarker(t *testing.T) {
	workDir := t.TempDir()
	if err := pinned.Save(workDir, ""); err != nil {
		t.Fatal(err)
	}
	updated := "stale pin"
	tool := NewPinContextTool(func(content string) { updated = content })
	tool.SetWorkDir(workDir)
	tool.LoadPersistedPin()
	if updated != "" {
		t.Fatalf("durable clear left active content %q", updated)
	}
}
