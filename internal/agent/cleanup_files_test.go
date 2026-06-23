package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/tools"
)

// Fix #4: cleanupOldResults must remove the backing output FILE before dropping
// a result's map entry — otherwise a long-running /loop (one agent per
// iteration) orphans hundreds of .log files. Once the map entry is gone the
// path is unrecoverable, so the removal must happen at eviction time.
func TestCleanupOldResults_RemovesOrphanedOutputFiles(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	dir := t.TempDir()

	n := MaxAgentResults + 12 // exceed the cap so eviction runs
	runner.mu.Lock()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("agent-%04d", i)
		f := filepath.Join(dir, id+".log")
		if err := os.WriteFile(f, []byte("output"), 0o644); err != nil {
			runner.mu.Unlock()
			t.Fatalf("write temp output: %v", err)
		}
		runner.results[id] = &AgentResult{AgentID: id, Completed: true, OutputFile: f}
	}
	runner.mu.Unlock()

	runner.cleanupOldResults()

	runner.mu.Lock()
	remaining := len(runner.results)
	live := make([]string, 0, remaining)
	for _, r := range runner.results {
		live = append(live, r.OutputFile)
	}
	runner.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	onDisk := len(entries)

	if remaining != MaxAgentResults/2 {
		t.Fatalf("remaining results = %d, want %d", remaining, MaxAgentResults/2)
	}
	// The invariant: exactly one file on disk per surviving result — the evicted
	// results' files were removed, nothing leaked.
	if onDisk != remaining {
		t.Errorf("files on disk = %d but %d results remain — %d orphaned file(s) leaked", onDisk, remaining, onDisk-remaining)
	}
	for _, f := range live {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("a surviving result's output file was wrongly removed: %s", f)
		}
	}
}
