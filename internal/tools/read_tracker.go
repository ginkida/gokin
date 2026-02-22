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
	Offset     int
	Limit      int
	ContentLen int
}

// FileReadTracker deduplicates file reads by tracking what was read and when.
// If the same file range is requested again and the file hasn't changed (same ModTime+Size),
// the executor replaces the full content with a short stub, saving context window space.
type FileReadTracker struct {
	mu      sync.RWMutex
	records map[string]*FileReadRecord // key: "filepath|offset|limit"
	turnSeq int
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
			// Update record, not a duplicate
			t.records[key] = &FileReadRecord{
				FilePath:   filePath,
				ModTime:    modTime,
				Size:       size,
				TurnIndex:  t.turnSeq,
				Offset:     offset,
				Limit:      limit,
				ContentLen: contentLen,
			}
			return false, existing, true
		}
		// Same file, same content — duplicate
		return true, existing, false
	}

	// First read — record it
	t.records[key] = &FileReadRecord{
		FilePath:   filePath,
		ModTime:    modTime,
		Size:       size,
		TurnIndex:  t.turnSeq,
		Offset:     offset,
		Limit:      limit,
		ContentLen: contentLen,
	}
	return false, nil, false
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
}
