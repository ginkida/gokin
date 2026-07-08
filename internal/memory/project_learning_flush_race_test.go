package memory

import (
	"os"
	"testing"
	"time"
)

func TestProjectLearningFlush_WaitsForInFlightDebouncedWrite(t *testing.T) {
	origInterval := projectLearningSaveDebounceInterval
	origHook := projectLearningSaveIOHookForTest
	t.Cleanup(func() {
		projectLearningSaveDebounceInterval = origInterval
		projectLearningSaveIOHookForTest = origHook
	})
	projectLearningSaveDebounceInterval = 10 * time.Millisecond

	dir := t.TempDir()
	pl, err := NewProjectLearning(dir)
	if err != nil {
		t.Fatalf("NewProjectLearning: %v", err)
	}

	hookEntered := make(chan struct{})
	proceedHook := make(chan struct{})
	projectLearningSaveIOHookForTest = func() {
		close(hookEntered)
		<-proceedHook
	}

	pl.SetPreference("fact:deploy-window", "deployments happen after 18:00 UTC")

	select {
	case <-hookEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("debounced project-learning save never reached the write phase")
	}

	if _, err := os.Stat(pl.Path()); err == nil {
		t.Fatal("test setup invalid: learning.yaml exists before the write hook was released")
	}

	flushDone := make(chan error, 1)
	go func() { flushDone <- pl.Flush() }()

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

	data, err := os.ReadFile(pl.Path())
	if err != nil {
		t.Fatalf("expected learning.yaml to exist after Flush returned: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("learning.yaml is empty after Flush returned")
	}
}
