package logging

import (
	"errors"
	"os"
	"sync"

	"gokin/internal/securefs"
)

var errLogFileUnavailable = errors.New("log file became unavailable during rotation")

// rotatingFileWriter bounds a diagnostic log DURING a run.
//
// Rotation used to happen only when the log file was opened, which never fires
// for the documented default path (a fresh `gokin-<timestamp>-<pid>.jsonl` per
// process) and never fires mid-run for an explicit `--debug-file` either. A
// detached `--bg` worker running for hours with `--debug` could therefore write
// an unbounded file, and the documented 10 MiB cap was true only across
// restarts.
//
// One backup is kept (`<path>.old`), matching what the open-time rotation did.
type rotatingFileWriter struct {
	mu    sync.Mutex
	file  *os.File
	path  string
	size  int64
	limit int64
}

func newRotatingFileWriter(file *os.File, path string, size, limit int64) *rotatingFileWriter {
	if limit <= 0 {
		limit = maxLogFileSize
	}
	return &rotatingFileWriter{file: file, path: path, size: size, limit: limit}
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return len(p), nil
	}
	// Rotate BEFORE the write that would cross the limit, so a single record is
	// never split across two files.
	if w.size > 0 && w.size+int64(len(p)) > w.limit {
		w.rotateLocked()
	}
	if w.file == nil {
		return 0, errLogFileUnavailable
	}
	written, err := w.file.Write(p)
	w.size += int64(written)
	return written, err
}

// rotateLocked is best effort: only the RENAME is skipped when the path cannot
// be proven to still be the file we were writing to. The reopen is always
// attempted, because OpenPrivateAppend re-validates the path itself — a hostile
// swap still fails closed there, while a benign external rotation (a second
// Gokin process, logrotate, a manual mv of the shared gokin.log) reattaches
// instead of killing diagnostics for the rest of the run.
//
// Returning early from every unverifiable branch was the bug this replaced: it
// left w.file nil, and Write then discarded every later record — in exactly the
// long-running detached case this rotator exists for.
func (w *rotatingFileWriter) rotateLocked() {
	if w.file == nil || w.path == "" {
		return
	}
	expected, statErr := w.file.Stat()
	backup := w.path + ".old"
	if err := w.file.Close(); err != nil {
		// The descriptor is unusable either way; fall through and try to reopen.
		_ = err
	}
	w.file = nil

	current, pathErr := os.Lstat(w.path)
	ours := statErr == nil && pathErr == nil &&
		current.Mode()&os.ModeSymlink == 0 && current.Mode().IsRegular() &&
		os.SameFile(expected, current)
	if ours {
		_ = os.Remove(backup)
		if err := os.Rename(w.path, backup); err == nil {
			_ = securefs.SecurePrivateFile(backup)
		}
	}
	file, err := securefs.OpenPrivateAppend(w.path)
	if err != nil {
		w.size = 0
		return
	}
	w.file = file
	if info, err := file.Stat(); err == nil {
		w.size = info.Size()
	} else {
		w.size = 0
	}
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
