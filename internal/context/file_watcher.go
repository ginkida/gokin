package context

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileWatcher watches a file or directory for changes.
type FileWatcher struct {
	path       string
	debounceMs int
	callback   func(string)
	cancel     context.CancelFunc
	done       chan struct{} // Signals goroutine completion

	mu      sync.Mutex
	lastMod time.Time
	timer   *time.Timer
	closed  bool
}

// NewFileWatcher creates a new file watcher.
func NewFileWatcher(ctx context.Context, path string, debounceMs int, callback func(string)) (*FileWatcher, error) {
	if debounceMs <= 0 {
		debounceMs = 500 // Default 500ms debounce
	}

	fw := &FileWatcher{
		path:       path,
		debounceMs: debounceMs,
		callback:   callback,
		done:       make(chan struct{}),
	}

	ctx, cancel := context.WithCancel(ctx)
	fw.cancel = cancel

	// Get initial mod time
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if info != nil {
		fw.lastMod = info.ModTime()
	}

	// Start watching goroutine
	go fw.watch(ctx)

	return fw, nil
}

// watch periodically checks for file changes.
func (fw *FileWatcher) watch(ctx context.Context) {
	defer close(fw.done) // Signal completion when goroutine exits

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fw.checkChanges()
		}
	}
}

// checkChanges checks if the file has been modified.
func (fw *FileWatcher) checkChanges() {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	info, err := os.Stat(fw.path)
	if err != nil {
		// File might not exist yet, that's ok
		return
	}

	modTime := info.ModTime()
	if !modTime.Equal(fw.lastMod) && modTime.Sub(fw.lastMod) > 100*time.Millisecond {
		// File changed
		fw.lastMod = modTime

		// Debounce
		if fw.timer != nil {
			fw.timer.Stop()
		}

		fw.timer = time.AfterFunc(time.Duration(fw.debounceMs)*time.Millisecond, func() {
			fw.mu.Lock()
			closed := fw.closed
			fw.mu.Unlock()
			if !closed {
				fw.callback(fw.path)
			}
		})
	}
}

// UpdatePath rebinds the watcher to a new target (e.g. once a fallback
// directory-watch discovers the concrete instruction file it was waiting
// for). Resets the baseline mtime to the new path's current state so the
// rebind itself doesn't spuriously fire the callback — only a REAL
// subsequent change does. Without a way to rebind, a watcher that started
// watching a directory (no instruction file existed yet) stays bound to the
// directory forever: a directory's mtime only changes on child add/remove/
// rename, never on content edits to an already-existing child, so every
// edit after the file's first creation is silently invisible.
func (fw *FileWatcher) UpdatePath(path string) {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	fw.path = path
	if info, err := os.Stat(path); err == nil {
		fw.lastMod = info.ModTime()
	} else {
		fw.lastMod = time.Time{}
	}
}

// Path returns the watcher's current target (for tests / diagnostics).
func (fw *FileWatcher) Path() string {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return fw.path
}

// Close stops the file watcher and waits for the goroutine to finish.
func (fw *FileWatcher) Close() {
	if fw.cancel != nil {
		fw.cancel()
	}

	// Wait for the watch goroutine to finish with timeout
	if fw.done != nil {
		closeTimer := time.NewTimer(5 * time.Second)
		select {
		case <-fw.done:
			closeTimer.Stop()
			// Goroutine finished cleanly
		case <-closeTimer.C:
			// Timeout - goroutine may be stuck
		}
	}

	fw.mu.Lock()
	fw.closed = true
	if fw.timer != nil {
		fw.timer.Stop()
	}
	fw.mu.Unlock()
}

// WatchInstructionFiles watches all possible instruction file locations.
func WatchInstructionFiles(ctx context.Context, workDir string, debounceMs int, callback func()) error {
	// Watch the directory containing instruction files
	watchDir := workDir
	watcher, err := NewFileWatcher(ctx, watchDir, debounceMs, func(changedPath string) {
		// Check if any instruction file exists and changed
		for _, filename := range instructionFiles {
			path := filepath.Join(workDir, filename)
			if changedPath == path || changedPath == workDir {
				callback()
				return
			}
		}
	})

	if err != nil {
		return err
	}

	// Keep watcher alive until context is done
	go func() {
		<-ctx.Done()
		watcher.Close()
	}()

	return nil
}
