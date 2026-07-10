package ui

import (
	"os"
	"sync"
	"testing"
	"time"
)

// TestCommandHistory_SaveRaceWithRecordUsage hammers RecordUsage — which
// mutates shared *HistoryEntry fields (Count++, Timestamp) under the write
// lock AND spawns async save goroutines that marshal the same entries — so
// -race flags the pre-fix pointer-snapshot save (json.Marshal read the
// pointees' fields outside the lock). The fix snapshots entry VALUES under
// the lock.
func TestCommandHistory_SaveRaceWithRecordUsage(t *testing.T) {
	// Manual temp dir with retrying cleanup: RecordUsage's fire-and-forget
	// `go ch.save()` goroutines can still be writing when the test body
	// returns, and t.TempDir's own RemoveAll fails with "directory not empty"
	// if a straggler recreates a file mid-removal.
	dir, err := os.MkdirTemp("", "palette-race")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for i := 0; i < 40; i++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	ch := &CommandHistory{
		entries:  make(map[string]*HistoryEntry),
		filePath: dir + "/history.json",
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ch.RecordUsage("cmd")
			}
		}()
	}
	wg.Wait()
}
