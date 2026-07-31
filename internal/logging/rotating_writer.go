package logging

import (
	"os"
	"sync"
)

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
	written, err := w.file.Write(p)
	w.size += int64(written)
	return written, err
}

// rotateLocked is best effort: a failed rename or reopen leaves the current
// file in place and logging continues. Losing diagnostics is worse than an
// oversized file.
func (w *rotatingFileWriter) rotateLocked() {
	if w.file == nil || w.path == "" {
		return
	}
	backup := w.path + ".old"
	if err := w.file.Close(); err != nil {
		// The descriptor is unusable either way; fall through and try to reopen.
		_ = err
	}
	_ = os.Remove(backup)
	if err := os.Rename(w.path, backup); err == nil {
		_ = os.Chmod(backup, 0o600)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		w.file = nil
		w.size = 0
		return
	}
	_ = file.Chmod(0o600)
	w.file = file
	w.size = 0
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
