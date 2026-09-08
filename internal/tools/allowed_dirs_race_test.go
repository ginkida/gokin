package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A directory grant widens the allowed scope while tools may be mid-execution:
// /add-dir runs on the command goroutine, the executor's ask-on-access prompt
// grants during a batch, and parallel-safe tools share one instance across
// concurrent calls. The setters used to swap the whole *PathValidator on the
// tool, an unsynchronized pointer write; under -race this test reported
// WARNING: DATA RACE against every concurrent read.
//
// Only -race can fail this, which is how it is run in CI.
func TestSetAllowedDirs_ConcurrentWithExecution(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// read is parallel-safe, so this is the shape the executor actually runs.
	tool := NewReadTool(dir)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			tool.SetAllowedDirs([]string{dir})
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			if _, err := tool.Execute(context.Background(), map[string]any{"file_path": file}); err != nil {
				t.Errorf("read during a grant: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// The same shape for a write tool: grants reach every path-scoped tool, not
// only the ones the executor runs in parallel.
func TestSetAllowedDirs_ConcurrentWithWriteToolValidation(t *testing.T) {
	dir := t.TempDir()
	tool := NewWriteTool(dir)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			tool.SetAllowedDirs([]string{dir})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 200 {
			_, _ = tool.Execute(context.Background(), map[string]any{
				"file_path": filepath.Join(dir, "out.txt"),
				"content":   "x",
			})
			_ = i
		}
	}()
	wg.Wait()
}
