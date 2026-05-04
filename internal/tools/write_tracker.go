package tools

import (
	"sync"
	"time"
)

// FileWriteTracker records which files the agent has written or edited in the
// current session. Used by injectContinuationHint to remind the model of
// already-modified files after context compaction — "these were written, don't
// overwrite unless the user asked for a change".
type FileWriteTracker struct {
	mu      sync.RWMutex
	entries []writeEntry
	seq     int
}

type writeEntry struct {
	path      string
	seq       int
	timestamp time.Time
}

// NewFileWriteTracker creates a new tracker.
func NewFileWriteTracker() *FileWriteTracker {
	return &FileWriteTracker{}
}

// Record marks a file path as modified. Deduplicates by path (updates seq
// so the path stays at the "top" of the most-recently-modified list).
func (t *FileWriteTracker) Record(path string) {
	if path == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	for i, e := range t.entries {
		if e.path == path {
			t.entries[i].seq = t.seq
			t.entries[i].timestamp = time.Now()
			return
		}
	}
	t.entries = append(t.entries, writeEntry{path: path, seq: t.seq, timestamp: time.Now()})
}

// RecentlyModifiedFiles returns up to limit distinct paths, most recently
// modified first.
func (t *FileWriteTracker) RecentlyModifiedFiles(limit int) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	type item struct {
		path string
		seq  int
	}
	items := make([]item, len(t.entries))
	for i, e := range t.entries {
		items[i] = item{e.path, e.seq}
	}
	// Sort by seq desc (most recent first). Small n, insertion sort.
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j-1].seq < items[j].seq; j-- {
			items[j-1], items[j] = items[j], items[j-1]
		}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.path
	}
	return paths
}

// Reset clears all tracked writes (called on /clear).
func (t *FileWriteTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = nil
	t.seq = 0
}
