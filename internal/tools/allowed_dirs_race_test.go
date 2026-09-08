package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gokin/internal/testkit"
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
	dir := testkit.ResolvedTempDir(t)
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
	dir := testkit.ResolvedTempDir(t)
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
		for range 200 {
			// Asserted, because an unresolved temp dir made an earlier version
			// of this test fail path validation on every call — it never
			// reached the field it was meant to race, and reported nothing.
			result, err := tool.Execute(context.Background(), map[string]any{
				"file_path": filepath.Join(dir, "out.txt"),
				"content":   "x",
			})
			if err != nil || !result.Success {
				t.Errorf("write during a grant: %v %q", err, result.Error)
				return
			}
		}
	}()
	wg.Wait()
}

// The same propagation block pushes diff-preview state into edit and write on
// every permission change — a YOLO toggle, /set permissions, ApplyConfig — from
// the app goroutine while a turn may be running. The executor's own flag from
// that block is an atomic.Bool and bash guards its copy with policyMu; this one
// was a bare bool, and -race reports it.
func TestSetDiffEnabled_ConcurrentWithExecution(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	tool := NewWriteTool(dir)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 300 {
			tool.SetDiffEnabled(i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for range 300 {
			result, err := tool.Execute(context.Background(), map[string]any{
				"file_path": filepath.Join(dir, "f.txt"),
				"content":   "x",
			})
			if err != nil || !result.Success {
				t.Errorf("write during a permission change: %v %q", err, result.Error)
				return
			}
		}
	}()
	wg.Wait()
}
