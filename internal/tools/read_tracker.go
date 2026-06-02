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
	// DupCount counts how many times this exact range was re-requested while
	// the file stayed unchanged. 0 = first read, 1 = first re-read, etc. It is
	// only ever mutated under t.mu; callers must read the snapshot returned by
	// CheckAndRecord, NOT this field on the shared pointer (it would race).
	DupCount int
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

// dedupReadStub decides what a duplicate read of an unchanged range should
// return. It implements a self-healing policy so the dedup optimisation can
// never starve the model of content it genuinely needs (the failure mode that
// used to dead-end in a re-read loop and a fatal stagnation abort):
//
//   - dupCount == 1 (first re-read): return a compact, actionable stub. This is
//     the common, benign "model glanced back" case — keep the token savings but
//     tell the model the content is still in context so it stops re-reading.
//   - dupCount == 2 (second re-read): return ok=false so the caller keeps the
//     FULL content. A model re-requesting the exact same range twice has lost
//     track of it; re-sending once is far cheaper than the stagnation abort the
//     loop would otherwise trigger.
//   - dupCount >= 3 (third+ re-read): stub again. This is now a genuine loop,
//     not amnesia — the executor's stagnation recovery (which re-attaches the
//     cached content with a firm "stop looping" hint) is the right backstop, and
//     re-injecting the full file on every iteration would just burn the budget.
func dedupReadStub(origRec *FileReadRecord, filePath string, dupCount int) (string, bool) {
	if origRec == nil {
		return "", false
	}
	if dupCount == 2 {
		return "", false
	}
	return fmt.Sprintf(
		"[Unchanged since turn %d · %d chars · %s] — already in context above; reuse it instead of re-reading. To see other code, change offset/limit or read another file; if you were about to edit, make the edit now.",
		origRec.TurnIndex, origRec.ContentLen, filePath), true
}

// CheckAndRecord checks whether this read is a duplicate.
// Returns (isDuplicate, originalRecord, fileChanged, dupCount).
// If the file was read before with identical ModTime+Size, isDuplicate is true
// and dupCount is the number of times this exact range has now been re-requested
// (1 on the first re-read, 2 on the second, …). dupCount is computed under the
// lock so callers never read the racy DupCount field off the shared *origRec.
// If the file exists but ModTime/Size changed since last read, fileChanged is true
// and the old record is replaced (dupCount resets to 0).
func (t *FileReadTracker) CheckAndRecord(filePath string, offset, limit, contentLen int) (bool, *FileReadRecord, bool, int) {
	info, err := os.Stat(filePath)
	if err != nil {
		// Can't stat — skip dedup, don't record
		return false, nil, false, 0
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
			return false, existing, true, 0
		}
		// Same file, same content — duplicate. Bump and snapshot the count
		// under the lock; the returned int is race-free even though the
		// pointer's field keeps mutating on later calls.
		existing.DupCount++
		return true, existing, false, existing.DupCount
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
	return false, nil, false, 0
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
