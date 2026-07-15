package memory

import (
	"os"
	"testing"
	"time"
)

// TestStoreFlush_WaitsForInFlightDebouncedWrite (round 5) pins the fix for a
// TOCTOU in Store.Flush(): the debounced save's timer callback clears
// s.dirty BEFORE it does its actual disk I/O (see scheduleSave), and
// Timer.Stop() cannot cancel a callback that has already started running.
// So Flush() could previously acquire s.mu right after the callback released
// it, observe dirty==false, and return nil ("nothing to do") while the
// callback's AtomicWrite calls were still in flight — a caller treating a
// nil-error Flush() as "safe to exit" (e.g. graceful shutdown, which this
// round also wired Flush() into) could terminate before the write actually
// landed on disk, silently losing the just-remembered fact.
//
// This uses the saveIOHookForTest seam to pause the debounce callback AFTER
// it has cleared dirty (mid write-phase, holding ioMu) and proves Flush()
// blocks until that write-phase completes, and that the data is genuinely
// on disk by the time Flush() returns.
func TestStoreFlush_WaitsForInFlightDebouncedWrite(t *testing.T) {
	saveTestSeamMu.Lock()
	origInterval := saveDebounceInterval
	origHook := saveIOHookForTest
	saveDebounceInterval = 10 * time.Millisecond
	saveTestSeamMu.Unlock()
	t.Cleanup(func() {
		saveTestSeamMu.Lock()
		saveDebounceInterval = origInterval
		saveIOHookForTest = origHook
		saveTestSeamMu.Unlock()
	})

	configDir, err := os.MkdirTemp("", "store-flush-race-config-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(configDir)
	projectPath, err := os.MkdirTemp("", "store-flush-race-project-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(projectPath)

	store, err := NewStore(configDir, projectPath, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	hookEntered := make(chan struct{})
	proceedHook := make(chan struct{})
	saveTestSeamMu.Lock()
	saveIOHookForTest = func() {
		close(hookEntered)
		<-proceedHook
	}
	saveTestSeamMu.Unlock()

	entry := NewEntry("the deploy key rotates every 90 days", MemoryProject)
	if err := store.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Wait for the debounce timer to fire and reach the write-phase hook —
	// proves the callback has ALREADY cleared s.dirty and released s.mu, and
	// is now paused right before actually writing to disk.
	select {
	case <-hookEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("debounced save never reached the write-phase hook")
	}

	// The file must not exist yet — the callback is paused before its first
	// saveFile call.
	if _, err := os.Stat(store.storagePath()); err == nil {
		t.Fatal("test setup invalid: storage file exists before the write-phase hook released")
	}

	flushDone := make(chan error, 1)
	go func() { flushDone <- store.Flush() }()

	// Flush() must BLOCK while the debounced write is in flight — it must
	// not return "nothing to do" just because dirty is already false.
	select {
	case err := <-flushDone:
		t.Fatalf("Flush() returned (err=%v) before the in-flight debounced write completed — this is the TOCTOU: Flush observed dirty==false while data was still unwritten", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: Flush() is still blocked on ioMu.
	}

	close(proceedHook)

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("Flush() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Flush() never returned after the write-phase hook was released")
	}

	// By the time Flush() returned, the debounced write must have actually
	// landed on disk.
	data, err := os.ReadFile(store.storagePath())
	if err != nil {
		t.Fatalf("expected the storage file to exist once Flush() returned: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("storage file is empty after Flush() returned")
	}
}
