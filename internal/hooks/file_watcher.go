package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gokin/internal/logging"
)

type FileChange struct{ Path, Event string }

type watchedFile struct{ info os.FileInfo }

// FileWatcher watches explicit files, including files outside the project and
// files whose parents do not exist yet. Parent watches survive atomic saves;
// a bounded metadata rescan also repairs replaced/moved parent directories.
// The callback runs separately so slow hooks never stop filesystem intake.
type FileWatcher struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	fs      *fsnotify.Watcher
	files   map[string]watchedFile
	pending map[string]FileChange
	changed chan struct{}
	wake    chan struct{}
	handle  func(context.Context, FileChange)
	wg      sync.WaitGroup
	close   sync.Once
	done    chan struct{}
}

func NewFileWatcher(ctx context.Context, handle func(context.Context, FileChange)) *FileWatcher {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &FileWatcher{ctx: ctx, cancel: cancel, files: map[string]watchedFile{}, pending: map[string]FileChange{}, changed: make(chan struct{}, 1), wake: make(chan struct{}, 1), handle: handle, done: make(chan struct{})}
	w.wg.Add(2)
	go w.observe()
	go w.deliver()
	go func() { w.wg.Wait(); close(w.done) }()
	return w
}

func (w *FileWatcher) SetPaths(paths []string) error {
	if err := ValidateWatchPaths(paths); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ctx.Err(); err != nil {
		return err
	}
	next := make(map[string]watchedFile, len(paths))
	for _, path := range paths {
		path = filepath.Clean(path)
		if current, ok := w.files[path]; ok {
			next[path] = current
		} else {
			info, _ := os.Stat(path)
			if info != nil && info.IsDir() {
				info = nil
			}
			next[path] = watchedFile{info: info}
		}
	}
	w.files = next
	for path := range w.pending {
		if _, ok := next[path]; !ok {
			delete(w.pending, path)
		}
	}
	err := w.reconcileLocked()
	select {
	case w.changed <- struct{}{}:
	default:
	}
	return err
}

func (w *FileWatcher) Contains(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.files[filepath.Clean(path)]
	return ok && w.ctx.Err() == nil
}

func watchAliases(path string) []string {
	aliases := []string{path}
	if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		aliases = append(aliases, filepath.Join(parent, filepath.Base(path)))
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		aliases = append(aliases, resolved)
	}
	return aliases
}

func nearestWatchDirectory(path string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return ""
		}
		path = parent
	}
}

func (w *FileWatcher) reconcileLocked() error {
	if w.fs == nil {
		if len(w.files) == 0 {
			return nil
		}
		fs, err := fsnotify.NewWatcher()
		if err != nil {
			return err
		}
		w.fs = fs
	}
	wanted := map[string]bool{}
	for path := range w.files {
		for _, alias := range watchAliases(path) {
			if dir := nearestWatchDirectory(filepath.Dir(alias)); dir != "" {
				wanted[dir] = true
			}
		}
	}
	current := map[string]bool{}
	for _, dir := range w.fs.WatchList() {
		current[dir] = true
		if !wanted[dir] {
			_ = w.fs.Remove(dir)
		}
	}
	var first error
	for dir := range wanted {
		if !current[dir] {
			if err := w.fs.Add(dir); err != nil && first == nil {
				first = fmt.Errorf("watch %s: %w", dir, err)
			}
		}
	}
	return first
}

func (w *FileWatcher) scan(dirty map[string]bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx.Err() != nil {
		return
	}
	for path, previous := range w.files {
		info, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			continue // An inaccessible file has not necessarily been deleted.
		}
		if info != nil && info.IsDir() {
			info = nil
		}
		kind := ""
		switch {
		case previous.info == nil && info != nil:
			kind = "add"
		case previous.info != nil && info == nil:
			kind = "unlink"
		case info != nil:
			if !os.SameFile(previous.info, info) || previous.info.Size() != info.Size() || !previous.info.ModTime().Equal(info.ModTime()) || previous.info.Mode() != info.Mode() {
				kind = "change"
			} else {
				for _, alias := range watchAliases(path) {
					if dirty[alias] || dirty[""] {
						kind = "change"
						break
					}
				}
			}
		}
		w.files[path] = watchedFile{info: info}
		if kind != "" {
			w.pending[path] = FileChange{path, kind}
		}
	}
	_ = w.reconcileLocked()
	if len(w.pending) > 0 {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (w *FileWatcher) observe() {
	defer w.wg.Done()
	defer w.stop()
	defer func() {
		if failure := recover(); failure != nil {
			logging.Error("hook file observer panicked", "panic", failure)
		}
	}()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	dirty := map[string]bool{}
	lastScan := time.Now()
	for {
		w.mu.Lock()
		var events <-chan fsnotify.Event
		var failures <-chan error
		if w.fs != nil {
			events, failures = w.fs.Events, w.fs.Errors
		}
		w.mu.Unlock()
		select {
		case <-w.ctx.Done():
			return
		case <-w.changed:
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Remove) {
				if len(dirty) < MaxFileWatchPaths*4 {
					dirty[filepath.Clean(event.Name)] = true
				} else {
					dirty[""] = true
				}
			}
		case err, ok := <-failures:
			if !ok {
				return
			}
			logging.Warn("hook file watcher", "error", err)
			dirty[""] = true // Rescan conservatively after a kernel queue overflow.
		case <-tick.C:
			if len(dirty) > 0 || time.Since(lastScan) >= time.Second {
				w.scan(dirty)
				dirty = map[string]bool{}
				lastScan = time.Now()
			}
		}
	}
}

func (w *FileWatcher) deliver() {
	defer w.wg.Done()
	defer w.cancel()
	defer func() {
		if failure := recover(); failure != nil {
			logging.Error("hook file delivery panicked", "panic", failure)
		}
	}()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		}
		for w.ctx.Err() == nil {
			w.mu.Lock()
			paths := make([]string, 0, len(w.pending))
			for path := range w.pending {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			if len(paths) == 0 {
				w.mu.Unlock()
				break
			}
			change := w.pending[paths[0]]
			delete(w.pending, paths[0])
			_, current := w.files[change.Path]
			w.mu.Unlock()
			if current && w.handle != nil {
				func() {
					defer func() {
						if failure := recover(); failure != nil {
							logging.Error("file hook callback panicked", "panic", failure)
						}
					}()
					w.handle(w.ctx, change)
				}()
			}
		}
	}
}

func (w *FileWatcher) Close() {
	if w == nil {
		return
	}
	w.stop()
	select {
	case <-w.done:
	case <-time.After(4 * time.Second):
		logging.Warn("hook file watcher cleanup exceeded wait limit")
	}
}

func (w *FileWatcher) stop() {
	w.close.Do(func() {
		w.cancel()
		w.mu.Lock()
		if w.fs != nil {
			_ = w.fs.Close()
		}
		w.mu.Unlock()
	})
}
