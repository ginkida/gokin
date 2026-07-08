package memory

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestExampleStoreFlush_WaitsForQueuedAsyncWritesAndSavesLatestSnapshot(t *testing.T) {
	origHook := exampleStoreSaveIOHookForTest
	t.Cleanup(func() {
		exampleStoreSaveIOHookForTest = origHook
	})

	store, err := NewExampleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewExampleStore: %v", err)
	}

	hookEntered := make(chan struct{})
	proceedHook := make(chan struct{})
	var hookCalls int
	exampleStoreSaveIOHookForTest = func() {
		hookCalls++
		if hookCalls == 1 {
			close(hookEntered)
			<-proceedHook
		}
	}

	if err := store.LearnFromSuccess("refactor", "first stale snapshot", "agent", "old result", time.Second, 10); err != nil {
		t.Fatalf("LearnFromSuccess first: %v", err)
	}

	select {
	case <-hookEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first async example-store save never reached the write phase")
	}

	if err := store.LearnFromSuccess("refactor", "second fresh snapshot", "agent", "new result", time.Second, 20); err != nil {
		t.Fatalf("LearnFromSuccess second: %v", err)
	}

	flushDone := make(chan error, 1)
	go func() { flushDone <- store.Flush() }()

	select {
	case err := <-flushDone:
		t.Fatalf("Flush() returned (err=%v) before queued async writes completed", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(proceedHook)

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("Flush() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Flush() never returned after queued async writes were released")
	}

	data, err := os.ReadFile(store.storagePath())
	if err != nil {
		t.Fatalf("expected examples.json to exist after Flush returned: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "first stale snapshot") || !strings.Contains(text, "second fresh snapshot") {
		t.Fatalf("examples.json does not contain the latest in-memory snapshot:\n%s", text)
	}
}
