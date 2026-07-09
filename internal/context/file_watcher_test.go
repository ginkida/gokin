package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ========== NewFileWatcher ==========

func TestNewFileWatcher_DefaultDebounce(t *testing.T) {
	dir := t.TempDir()
	fw, err := NewFileWatcher(context.Background(), dir, 0, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	defer fw.Close()

	if fw.debounceMs != 500 {
		t.Errorf("default debounce = %d, want 500", fw.debounceMs)
	}
}

func TestNewFileWatcher_CustomDebounce(t *testing.T) {
	dir := t.TempDir()
	fw, err := NewFileWatcher(context.Background(), dir, 300, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	defer fw.Close()

	if fw.debounceMs != 300 {
		t.Errorf("debounce = %d, want 300", fw.debounceMs)
	}
}

func TestNewFileWatcher_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	os.WriteFile(path, []byte("initial"), 0644)

	fw, err := NewFileWatcher(context.Background(), path, 100, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	defer fw.Close()

	if fw.lastMod.IsZero() {
		t.Error("lastMod should be set for existing file")
	}
}

func TestNewFileWatcher_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.txt")

	fw, err := NewFileWatcher(context.Background(), path, 100, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error for nonexistent file: %v", err)
	}
	defer fw.Close()

	// Nonexistent file is OK — lastMod stays zero
	if !fw.lastMod.IsZero() {
		t.Error("lastMod should be zero for nonexistent file")
	}
}

// ========== FileWatcher Close ==========

func TestFileWatcher_Close(t *testing.T) {
	dir := t.TempDir()
	fw, err := NewFileWatcher(context.Background(), dir, 100, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}

	// Close should complete without hanging (5s timeout internally)
	done := make(chan struct{})
	go func() {
		fw.Close()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(6 * time.Second):
		t.Fatal("Close timed out")
	}
}

func TestFileWatcher_CloseSetsClosedFlag(t *testing.T) {
	dir := t.TempDir()
	fw, err := NewFileWatcher(context.Background(), dir, 100, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	fw.Close()

	fw.mu.Lock()
	closed := fw.closed
	fw.mu.Unlock()

	if !closed {
		t.Error("closed flag should be true after Close")
	}
}

func TestFileWatcher_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	fw, err := NewFileWatcher(context.Background(), dir, 100, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}

	// Double close should not panic
	fw.Close()
	fw.Close()
}

// ========== FileWatcher change detection ==========

func TestFileWatcher_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watched.txt")
	os.WriteFile(path, []byte("initial"), 0644)

	var callbackCount int32
	var wg sync.WaitGroup
	wg.Add(1)

	fw, err := NewFileWatcher(context.Background(), path, 50, func(changed string) {
		atomic.AddInt32(&callbackCount, 1)
		wg.Done()
	})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	defer fw.Close()

	// Wait for callback with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	// Re-write the file periodically until the watcher reports it (bounded
	// by the outer timeout). A single write raced the fsnotify watch
	// registration on loaded CI runners — the event fired before the watch
	// was armed and the callback never came (first CI run of this test
	// failed exactly that way at 3s). Repeated distinct writes make the
	// test deterministic without caring HOW long registration takes.
	writeTicker := time.NewTicker(200 * time.Millisecond)
	defer writeTicker.Stop()
	timeout := time.After(10 * time.Second)
	i := 0
waitLoop:
	for {
		select {
		case <-done:
			break waitLoop // Callback fired
		case <-writeTicker.C:
			i++
			os.WriteFile(path, []byte(fmt.Sprintf("modified-%d", i)), 0644)
		case <-timeout:
			t.Fatal("callback was not invoked after file change")
		}
	}

	if count := atomic.LoadInt32(&callbackCount); count < 1 {
		t.Errorf("callback count = %d, want >= 1", count)
	}
}

func TestFileWatcher_NoCallbackOnNoChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stable.txt")
	os.WriteFile(path, []byte("stable"), 0644)

	var callbackCount int32

	fw, err := NewFileWatcher(context.Background(), path, 50, func(changed string) {
		atomic.AddInt32(&callbackCount, 1)
	})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	defer fw.Close()

	// Wait without modifying the file
	time.Sleep(600 * time.Millisecond)

	if count := atomic.LoadInt32(&callbackCount); count != 0 {
		t.Errorf("callback count = %d, want 0 (no change)", count)
	}
}

// ========== FileWatcher checkChanges ==========

func TestFileWatcher_CheckChanges_NonexistentFile(t *testing.T) {
	fw := &FileWatcher{
		path:       "/nonexistent/path/file.txt",
		debounceMs: 100,
	}
	// Should not panic when file doesn't exist — just silently return
	fw.checkChanges()

	// Verify no state was changed (lastMod stays zero)
	fw.mu.Lock()
	lastMod := fw.lastMod
	fw.mu.Unlock()
	if !lastMod.IsZero() {
		t.Error("lastMod should remain zero for nonexistent file")
	}
}

func TestFileWatcher_CheckChanges_DebounceStopsPreviousTimer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "debounce.txt")
	os.WriteFile(path, []byte("v1"), 0644)

	fw, err := NewFileWatcher(context.Background(), path, 200, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}
	defer fw.Close()

	// Rapid modifications — the debounce should coalesce them
	time.Sleep(100 * time.Millisecond)
	os.WriteFile(path, []byte("v2"), 0644)
	fw.checkChanges() // Sets timer

	os.WriteFile(path, []byte("v3"), 0644)
	fw.checkChanges() // Should stop previous timer, set new one

	// If debounce works, we don't get a panic from double-timer
}

// ========== WatchInstructionFiles ==========

func TestWatchInstructionFiles(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := WatchInstructionFiles(ctx, dir, 100, func() {})
	if err != nil {
		t.Fatalf("WatchInstructionFiles error: %v", err)
	}

	// Cancel should clean up the watcher
	cancel()
	time.Sleep(100 * time.Millisecond)
}

func TestWatchInstructionFiles_WithExistingFiles(t *testing.T) {
	dir := t.TempDir()

	// Create an instruction file
	for _, name := range instructionFiles {
		os.WriteFile(filepath.Join(dir, name), []byte("test"), 0644)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := WatchInstructionFiles(ctx, dir, 100, func() {})
	if err != nil {
		t.Fatalf("WatchInstructionFiles error: %v", err)
	}
}

// ========== FileWatcher context cancellation ==========

func TestFileWatcher_ContextCancellation(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	fw, err := NewFileWatcher(ctx, dir, 100, func(s string) {})
	if err != nil {
		t.Fatalf("NewFileWatcher error: %v", err)
	}

	// Cancel context — watch goroutine should stop
	cancel()

	// Close should still work (and not hang)
	done := make(chan struct{})
	go func() {
		fw.Close()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(6 * time.Second):
		t.Fatal("Close timed out after context cancellation")
	}
}
