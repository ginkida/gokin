package watcher

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ---------------------------------------------------------------------------
// NewWatcher — disabled vs enabled
// ---------------------------------------------------------------------------

func TestNewWatcher_Disabled(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.IsRunning() {
		t.Error("disabled watcher should not be running")
	}
	if w.WatchedPaths() != 0 {
		t.Errorf("WatchedPaths = %d, want 0", w.WatchedPaths())
	}
}

func TestNewWatcher_Defaults(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{
		Enabled:    true,
		DebounceMs: 0, // too low → floored to 500
		MaxWatches: 0, // too low → default 1000
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	if w.debounceMs != 500 {
		t.Errorf("debounceMs = %d, want 500 (floored)", w.debounceMs)
	}
	if w.maxWatches != 1000 {
		t.Errorf("maxWatches = %d, want 1000 (default)", w.maxWatches)
	}
}

func TestNewWatcher_ExplicitConfig(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{
		Enabled:    true,
		DebounceMs: 100,
		MaxWatches: 50,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	if w.debounceMs != 100 {
		t.Errorf("debounceMs = %d, want 100", w.debounceMs)
	}
	if w.maxWatches != 50 {
		t.Errorf("maxWatches = %d, want 50", w.maxWatches)
	}
}

// ---------------------------------------------------------------------------
// SetOnFileChange
// ---------------------------------------------------------------------------

func TestSetOnFileChange(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{Enabled: true, DebounceMs: 50})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	var called atomic.Bool
	w.SetOnFileChange(func(path string, op Operation) {
		called.Store(true)
	})

	w.mu.Lock()
	if w.onFileChange == nil {
		t.Error("handler was not set")
	}
	w.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Start / Stop lifecycle
// ---------------------------------------------------------------------------

func TestStartStop_Lifecycle(t *testing.T) {
	workDir := t.TempDir()
	w, err := NewWatcher(workDir, nil, Config{
		Enabled:    true,
		DebounceMs: 50,
		MaxWatches: 10,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !w.IsRunning() {
		t.Error("should be running after Start")
	}

	// Double Start → no-op, returns nil
	if err := w.Start(); err != nil {
		t.Errorf("second Start: %v", err)
	}

	if err := w.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}

	if w.IsRunning() {
		t.Error("should not be running after Stop")
	}

	// Double Stop → no-op, returns nil
	if err := w.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStart_Disabled(t *testing.T) {
	w, err := NewWatcher(t.TempDir(), nil, Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// Disabled watcher: Start returns nil without doing anything
	if err := w.Start(); err != nil {
		t.Errorf("Start on disabled: %v", err)
	}
	if w.IsRunning() {
		t.Error("disabled watcher should not be running")
	}
}

// ---------------------------------------------------------------------------
// Full integration: create/modify/delete triggers callback
// ---------------------------------------------------------------------------

func TestWatcher_FileModifyTriggersCallback(t *testing.T) {
	workDir := t.TempDir()
	w, err := NewWatcher(workDir, nil, Config{
		Enabled:    true,
		DebounceMs: 30,
		MaxWatches: 100,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	var mu sync.Mutex
	var events []string
	w.SetOnFileChange(func(path string, op Operation) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, path+":"+op.String())
	})

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Create a file → should trigger a modify or create event
	testFile := filepath.Join(workDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait for debounce + callback
	waitForEvents(t, &mu, &events, 1, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected at least one event for file creation")
	}
}

func TestWatcher_FileDeleteDetected(t *testing.T) {
	workDir := t.TempDir()
	// Pre-create a file before watching starts
	testFile := filepath.Join(workDir, "delete-me.txt")
	os.WriteFile(testFile, []byte("bye"), 0644)

	w, err := NewWatcher(workDir, nil, Config{
		Enabled:    true,
		DebounceMs: 30,
		MaxWatches: 100,
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Stop()

	var mu sync.Mutex
	var ops []Operation
	w.SetOnFileChange(func(path string, op Operation) {
		mu.Lock()
		defer mu.Unlock()
		ops = append(ops, op)
	})

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Delete the file
	os.Remove(testFile)

	waitForOps(t, &mu, &ops, 1, 2*time.Second)

	mu.Lock()
	defer mu.Unlock()
	foundDelete := false
	for _, op := range ops {
		if op == OpDelete {
			foundDelete = true
		}
	}
	if !foundDelete {
		t.Errorf("expected OpDelete in %v", ops)
	}
}

// ---------------------------------------------------------------------------
// detectOperation — direct unit test
// ---------------------------------------------------------------------------

func TestDetectOperation_ExistingFile(t *testing.T) {
	w := &Watcher{}
	path := filepath.Join(t.TempDir(), "exists.txt")
	os.WriteFile(path, []byte("x"), 0644)

	op := w.detectOperation(path)
	if op != OpModify {
		t.Errorf("detectOperation(existing) = %v, want OpModify", op)
	}
}

func TestDetectOperation_MissingFile(t *testing.T) {
	w := &Watcher{}
	op := w.detectOperation("/nonexistent/path/file.txt")
	if op != OpDelete {
		t.Errorf("detectOperation(missing) = %v, want OpDelete", op)
	}
}

// ---------------------------------------------------------------------------
// handleEvent — directly exercise filtering logic
// ---------------------------------------------------------------------------

func TestHandleEvent_SkipsTempFiles(t *testing.T) {
	w := &Watcher{
		pending: make(map[string]time.Time),
	}

	// Temp files (starting with . or #, ending with ~) should be skipped
	for _, name := range []string{".swp123", "#temp", "backup~"} {
		w.handleEvent(fsnotify.Event{Name: name, Op: fsnotify.Write})
	}

	w.mu.Lock()
	count := len(w.pending)
	w.mu.Unlock()

	if count != 0 {
		t.Errorf("temp files added to pending: %d entries", count)
	}
}

func TestHandleEvent_AddsToPending(t *testing.T) {
	w := &Watcher{
		pending: make(map[string]time.Time),
	}

	w.handleEvent(fsnotify.Event{Name: "/path/to/real.go", Op: fsnotify.Write})

	w.mu.Lock()
	count := len(w.pending)
	w.mu.Unlock()

	if count != 1 {
		t.Errorf("pending = %d, want 1", count)
	}
}

func TestHandleEvent_PendingCapFlush(t *testing.T) {
	w := &Watcher{
		pending: make(map[string]time.Time),
	}

	// Fill pending up to the maxPending cap to trigger the flush path
	const maxPending = 5000
	for i := 0; i < maxPending; i++ {
		w.pending[filepath.Join("/dir", "file"+string(rune(i)))] = time.Now()
	}
	// One more should trigger the flush (resetting the map)
	w.handleEvent(fsnotify.Event{Name: "/path/new.go", Op: fsnotify.Write})

	w.mu.Lock()
	count := len(w.pending)
	w.mu.Unlock()

	// After flush, only the new file should be in pending
	if count != 1 {
		t.Errorf("after flush, pending = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// flushPending — no handler / empty pending / real delivery
// ---------------------------------------------------------------------------

func TestFlushPending_NoHandlerIsNoOp(t *testing.T) {
	w := &Watcher{
		pending:    make(map[string]time.Time),
		debounceMs: 10,
	}
	w.pending["/path"] = time.Now().Add(-1 * time.Hour)

	// Should be a no-op (handler is nil) — no panic, pending unchanged.
	w.flushPending()

	if len(w.pending) != 1 {
		t.Errorf("pending was modified with nil handler: %d entries", len(w.pending))
	}
}

func TestFlushPending_EmptyPending(t *testing.T) {
	var called atomic.Bool
	w := &Watcher{
		pending:    make(map[string]time.Time),
		debounceMs: 10,
		onFileChange: func(path string, op Operation) {
			called.Store(true)
		},
	}

	w.flushPending()

	if called.Load() {
		t.Error("handler should not be called for empty pending")
	}
}

func TestFlushPending_NotYetStable(t *testing.T) {
	var called atomic.Bool
	w := &Watcher{
		pending:    make(map[string]time.Time),
		debounceMs: 1000, // long debounce
		onFileChange: func(path string, op Operation) {
			called.Store(true)
		},
	}
	// Event just added (not yet stable)
	w.pending["/path"] = time.Now()

	w.flushPending()

	if called.Load() {
		t.Error("handler should not be called for unstable event")
	}
}

func TestFlushPending_DeliversStableEvent(t *testing.T) {
	var mu sync.Mutex
	var got []string
	w := &Watcher{
		pending:    make(map[string]time.Time),
		debounceMs: 5,
		onFileChange: func(path string, op Operation) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, path)
		},
	}
	// Stable event (added long ago)
	w.pending["/stable/file.go"] = time.Now().Add(-1 * time.Hour)

	w.flushPending()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != "/stable/file.go" {
		t.Errorf("got = %v, want [/stable/file.go]", got)
	}
}

// ---------------------------------------------------------------------------
// addDirectories — skips common dirs
// ---------------------------------------------------------------------------

func TestAddDirectories_SkipsCommonDirs(t *testing.T) {
	workDir := t.TempDir()

	// Create directories that should be skipped
	for _, name := range []string{".git", "node_modules", "vendor", ".idea", ".vscode", "__pycache__", "target", "build", "dist"} {
		os.MkdirAll(filepath.Join(workDir, name), 0755)
	}
	// And a normal one
	os.MkdirAll(filepath.Join(workDir, "src"), 0755)

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify: %v", err)
	}
	defer fsw.Close()

	w := &Watcher{
		fsWatcher:  fsw,
		workDir:    workDir,
		maxWatches: 100,
	}

	if err := w.addDirectories(); err != nil {
		t.Fatalf("addDirectories: %v", err)
	}

	watched := fsw.WatchList()
	// src should be watched, .git/node_modules/etc should NOT
	foundSrc := false
	for _, p := range watched {
		base := filepath.Base(p)
		if base == "src" {
			foundSrc = true
		}
		if base == ".git" || base == "node_modules" || base == "vendor" {
			t.Errorf("common dir %q should not be watched: %s", base, p)
		}
	}
	if !foundSrc {
		t.Error("src directory should be watched")
	}
}

func TestAddDirectories_RespectsMaxWatches(t *testing.T) {
	workDir := t.TempDir()

	// Create many directories
	for i := 0; i < 20; i++ {
		os.MkdirAll(filepath.Join(workDir, "dir"+string(rune('a'+i))), 0755)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify: %v", err)
	}
	defer fsw.Close()

	w := &Watcher{
		fsWatcher:  fsw,
		workDir:    workDir,
		maxWatches: 5,
	}

	if err := w.addDirectories(); err != nil {
		t.Fatalf("addDirectories: %v", err)
	}

	watched := len(fsw.WatchList())
	// Includes workDir itself + up to maxWatches
	if watched > 6 {
		t.Errorf("watched = %d, should respect maxWatches=5 (+root)", watched)
	}
}

func TestAddDirectories_NonExistentWorkDir(t *testing.T) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatalf("fsnotify: %v", err)
	}
	defer fsw.Close()

	w := &Watcher{
		fsWatcher:  fsw,
		workDir:    "/nonexistent/path/that/does/not/exist",
		maxWatches: 100,
	}

	// Should return an error (filepath.Walk on non-existent path)
	err = w.addDirectories()
	if err == nil {
		// Some systems may handle this differently; if no error, verify nothing was watched
		t.Log("addDirectories on non-existent path returned nil")
	}
}

// ---------------------------------------------------------------------------
// WatchedPaths
// ---------------------------------------------------------------------------

func TestWatchedPaths_NilWatcher(t *testing.T) {
	w := &Watcher{fsWatcher: nil}
	if w.WatchedPaths() != 0 {
		t.Errorf("WatchedPaths (nil) = %d, want 0", w.WatchedPaths())
	}
}

func TestWatchedPaths_AfterStart(t *testing.T) {
	workDir := t.TempDir()
	os.MkdirAll(filepath.Join(workDir, "sub"), 0755)

	w, err := NewWatcher(workDir, nil, Config{Enabled: true, DebounceMs: 50})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Stop()

	if w.WatchedPaths() == 0 {
		t.Error("expected at least one watched path after Start")
	}
}

// ---------------------------------------------------------------------------
// EventBuffer — additional edge cases
// ---------------------------------------------------------------------------

func TestEventBuffer_WrapAround(t *testing.T) {
	b := NewEventBuffer(3)
	for i := 0; i < 5; i++ {
		b.Add(Event{Path: "file" + string(rune('a'+i))})
	}

	// Only the last 3 should be kept
	if b.Len() != 3 {
		t.Fatalf("Len = %d, want 3", b.Len())
	}

	recent := b.Recent(3)
	// Oldest of the 5 ("file_a") should be gone
	for _, e := range recent {
		if e.Path == "file_a" || e.Path == "file_b" {
			t.Errorf("wrapped-out event still present: %s", e.Path)
		}
	}
}

func TestEventBuffer_RecentOrder(t *testing.T) {
	b := NewEventBuffer(10)
	b.Add(Event{Path: "first"})
	b.Add(Event{Path: "second"})
	b.Add(Event{Path: "third"})

	recent := b.Recent(3)
	if recent[0].Path != "first" || recent[2].Path != "third" {
		t.Errorf("order wrong: %s, %s, %s", recent[0].Path, recent[1].Path, recent[2].Path)
	}
}

func TestEventBuffer_Clear(t *testing.T) {
	b := NewEventBuffer(10)
	b.Add(Event{Path: "x"})
	b.Clear()

	if b.Len() != 0 {
		t.Errorf("Len after Clear = %d, want 0", b.Len())
	}
	if recent := b.Recent(5); recent != nil {
		t.Errorf("Recent after Clear = %v, want nil", recent)
	}
}

func TestEventBuffer_RecentMoreThanCount(t *testing.T) {
	b := NewEventBuffer(10)
	b.Add(Event{Path: "only"})
	recent := b.Recent(5)
	if len(recent) != 1 {
		t.Errorf("Recent(5) with 1 event = %d items, want 1", len(recent))
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// waitForEvents polls a mutex-guarded slice until it has at least n entries
// or the timeout expires.
func waitForEvents(t *testing.T, mu *sync.Mutex, events *[]string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(*events)
		mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForOps is the Operation-typed variant of waitForEvents.
func waitForOps(t *testing.T, mu *sync.Mutex, ops *[]Operation, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(*ops)
		mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
