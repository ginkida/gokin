package tools

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// FileReadRecord stores metadata about a file read operation.
type FileReadRecord struct {
	FilePath   string
	ModTime    time.Time
	Size       int64
	TurnIndex  int
	Seq        int // monotonic per-record; breaks ties when multiple files share a turn
	Offset     int
	Limit      int
	ContentLen int
}

// FileReadTracker deduplicates file reads by tracking what was read and when.
// If the same file range is requested again and the file hasn't changed (same ModTime+Size),
// the executor replaces the full content with a short stub, saving context window space.
type FileReadTracker struct {
	mu       sync.RWMutex
	records  map[string]*FileReadRecord // key: "filepath|offset|limit"
	turnSeq  int
	writeSeq int
}

// NewFileReadTracker creates a new tracker.
func NewFileReadTracker() *FileReadTracker {
	return &FileReadTracker{
		records: make(map[string]*FileReadRecord),
	}
}

// makeKey builds the dedup key from path, offset, and limit.
func makeKey(filePath string, offset, limit int) string {
	return fmt.Sprintf("%s|%d|%d", filePath, offset, limit)
}

// CheckAndRecord checks whether this read is a duplicate.
// Returns (isDuplicate, originalRecord, fileChanged).
// If the file was read before with identical ModTime+Size, isDuplicate is true.
// If the file exists but ModTime/Size changed since last read, fileChanged is true
// and the old record is replaced.
func (t *FileReadTracker) CheckAndRecord(filePath string, offset, limit, contentLen int) (bool, *FileReadRecord, bool) {
	info, err := os.Stat(filePath)
	if err != nil {
		// Can't stat — skip dedup, don't record
		return false, nil, false
	}

	modTime := info.ModTime()
	size := info.Size()
	key := makeKey(filePath, offset, limit)

	t.mu.Lock()
	defer t.mu.Unlock()

	existing, found := t.records[key]
	if found {
		// File changed since last read?
		if existing.ModTime != modTime || existing.Size != size {
			t.writeSeq++
			t.records[key] = &FileReadRecord{
				FilePath:   filePath,
				ModTime:    modTime,
				Size:       size,
				TurnIndex:  t.turnSeq,
				Seq:        t.writeSeq,
				Offset:     offset,
				Limit:      limit,
				ContentLen: contentLen,
			}
			return false, existing, true
		}
		// Same file, same content — duplicate
		return true, existing, false
	}

	t.writeSeq++
	t.records[key] = &FileReadRecord{
		FilePath:   filePath,
		ModTime:    modTime,
		Size:       size,
		TurnIndex:  t.turnSeq,
		Seq:        t.writeSeq,
		Offset:     offset,
		Limit:      limit,
		ContentLen: contentLen,
	}
	return false, nil, false
}

// HasBeenRead reports whether the given (absolute) file path has any
// active read record in the session. Used by Edit to block blind edits:
// a model that greps a file and then tries to Edit it without Read has
// only seen short snippets and is likely to clobber surrounding context.
//
// After a write/edit, InvalidateFile drops records for the file, so this
// will return false again — the model is expected to Re-read before
// another Edit on the same file.
func (t *FileReadTracker) HasBeenRead(filePath string) bool {
	if filePath == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, rec := range t.records {
		if rec.FilePath == filePath {
			return true
		}
	}
	return false
}

// InvalidateFile removes all records for the given file path.
// Called after write/edit/delete operations on the file.
func (t *FileReadTracker) InvalidateFile(filePath string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key, rec := range t.records {
		if rec.FilePath == filePath {
			delete(t.records, key)
		}
	}
}

// IncrementTurn advances the turn counter.
func (t *FileReadTracker) IncrementTurn() {
	t.mu.Lock()
	t.turnSeq++
	t.mu.Unlock()
}

// Reset clears all records (e.g. on session clear/load).
func (t *FileReadTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = make(map[string]*FileReadRecord)
	t.turnSeq = 0
	t.writeSeq = 0
}

// RecentlyReadFiles returns up to `limit` distinct file paths that have been
// read in the current session, most recently read first. Used by the agent
// to remind the model after compaction: "these files were already loaded, no
// need to re-read unless something changed". Collapses multiple reads of the
// same file (different offset/limit ranges) into one entry.
func (t *FileReadTracker) RecentlyReadFiles(limit int) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Pick the latest Seq per file. Seq is per-record and monotonic across all
	// CheckAndRecord writes, so within the same turn the most recent write wins —
	// map iteration order can't perturb the ranking.
	seen := make(map[string]int, len(t.records))
	for _, rec := range t.records {
		if prev, ok := seen[rec.FilePath]; !ok || rec.Seq > prev {
			seen[rec.FilePath] = rec.Seq
		}
	}
	type entry struct {
		path string
		seq  int
	}
	list := make([]entry, 0, len(seen))
	for path, seq := range seen {
		list = append(list, entry{path, seq})
	}
	// Sort by seq desc (most recent first). Small n (≤ 40-ish), so insertion sort is fine.
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j-1].seq < list[j].seq; j-- {
			list[j-1], list[j] = list[j], list[j-1]
		}
	}
	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.path
	}
	return out
}
