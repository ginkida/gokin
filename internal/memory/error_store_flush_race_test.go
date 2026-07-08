package memory

import (
	"os"
	"testing"
	"time"
)

func TestErrorStoreFlush_WaitsForInFlightDebouncedWrite(t *testing.T) {
	origInterval := errorStoreSaveDebounceInterval
	origHook := errorStoreSaveIOHookForTest
	t.Cleanup(func() {
		errorStoreSaveDebounceInterval = origInterval
		errorStoreSaveIOHookForTest = origHook
	})
	errorStoreSaveDebounceInterval = 10 * time.Millisecond

	store, err := NewErrorStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewErrorStore: %v", err)
	}

	hookEntered := make(chan struct{})
	proceedHook := make(chan struct{})
	errorStoreSaveIOHookForTest = func() {
		close(hookEntered)
		<-proceedHook
	}

	if err := store.LearnError("go-test", "panic: nil pointer", "initialize dependency", []string{"go"}); err != nil {
		t.Fatalf("LearnError: %v", err)
	}

	select {
	case <-hookEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("debounced error-store save never reached the write phase")
	}

	if _, err := os.Stat(store.storagePath()); err == nil {
		t.Fatal("test setup invalid: errors.json exists before the write hook was released")
	}

	flushDone := make(chan error, 1)
	go func() { flushDone <- store.Flush() }()

	select {
	case err := <-flushDone:
		t.Fatalf("Flush() returned (err=%v) before the in-flight debounced write completed", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(proceedHook)

	select {
	case err := <-flushDone:
		if err != nil {
			t.Fatalf("Flush() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Flush() never returned after the write hook was released")
	}

	data, err := os.ReadFile(store.storagePath())
	if err != nil {
		t.Fatalf("expected errors.json to exist after Flush returned: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("errors.json is empty after Flush returned")
	}
}
