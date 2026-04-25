package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestQueueManager_PanicInExecute_OnCompleteFires verifies the
// disabled-queue immediate-execution path recovers from a panic in
// execute() and still calls onComplete with a synthesized error.
//
// Without the recovery, the goroutine crashes the process. Even if
// we recovered silently, the caller waiting on onComplete would hang
// forever because onComplete never fires. The fix synthesizes a
// panic-as-error so onComplete always runs exactly once.
func TestQueueManager_PanicInExecute_OnCompleteFires(t *testing.T) {
	qm := NewQueueManager(10)
	qm.SetEnabled(false) // exercises the immediate-path goroutine

	gotErr := make(chan error, 1)
	execute := func(ctx context.Context) error {
		panic("boom")
	}
	onComplete := func(err error) {
		gotErr <- err
	}

	if _, err := qm.Enqueue(t.Context(), "panicky-task", PriorityNormal, execute, onComplete); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case err := <-gotErr:
		if err == nil {
			t.Fatal("expected panic-as-error from onComplete, got nil")
		}
		if !strings.Contains(err.Error(), "panicked") {
			t.Errorf("expected error to mention panic, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onComplete did not fire within 2s — panic recovery missing or onComplete leaked")
	}
}

// TestQueueManager_NormalExecution_OnCompleteSeesError exercises the
// non-panic path to make sure the recovery wrapper doesn't swallow
// real errors.
func TestQueueManager_NormalExecution_OnCompleteSeesError(t *testing.T) {
	qm := NewQueueManager(10)
	qm.SetEnabled(false) // immediate path
	want := errors.New("real failure")

	gotErr := make(chan error, 1)
	execute := func(ctx context.Context) error { return want }
	onComplete := func(err error) { gotErr <- err }

	if _, err := qm.Enqueue(t.Context(), "normal-fail", PriorityNormal, execute, onComplete); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	select {
	case err := <-gotErr:
		if !errors.Is(err, want) {
			t.Errorf("expected real error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onComplete did not fire")
	}
}

// TestDependencyManager_PanicInTask_DoesNotCrash verifies that a
// panic in a task's Execute fn during ExecuteDependencies is
// recovered, the task is marked failed, and the parallel level
// completes (rather than wg.Done never firing → caller hangs).
func TestDependencyManager_PanicInTask_DoesNotCrash(t *testing.T) {
	dm := NewDependencyManager()

	// One panicky task and one healthy task at the same level — verifies
	// the panic of one doesn't poison the parallel sibling.
	healthyDone := make(chan struct{}, 1)
	if err := dm.AddTask(&DependencyTask{
		ID:      "panicky",
		Message: "will panic",
		Execute: func(ctx context.Context) error { panic("dependency boom") },
	}); err != nil {
		t.Fatalf("AddTask panicky: %v", err)
	}
	if err := dm.AddTask(&DependencyTask{
		ID:      "healthy",
		Message: "fine",
		Execute: func(ctx context.Context) error {
			healthyDone <- struct{}{}
			return nil
		},
	}); err != nil {
		t.Fatalf("AddTask healthy: %v", err)
	}

	// Execute with a tight deadline — if recovery is missing, wg never
	// counts down and the call hangs until ctx expires (or forever in
	// release builds where there's no test timeout).
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	var once sync.Once
	go func() {
		err := dm.ExecuteDependencies(ctx, 4)
		once.Do(func() { doneCh <- err })
	}()

	select {
	case <-doneCh:
		// Good — ExecuteDependencies returned. We don't assert error
		// shape here; the contract is "doesn't hang on panic" + "panicky
		// task is marked failed" (checked next).
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteDependencies hung after task panic — recovery missing")
	}

	// The healthy task must have run regardless of its sibling
	// panicking.
	select {
	case <-healthyDone:
	default:
		t.Error("healthy task did not run alongside panicky sibling")
	}

	// The panicky task must be marked failed (not stuck in Running).
	task, ok := dm.GetTask("panicky")
	if !ok {
		t.Fatal("panicky task vanished from manager")
	}
	if task.Status != TaskStatusFailed {
		t.Errorf("panicky task status = %v, want TaskStatusFailed", task.Status)
	}
	if task.Error == nil || !strings.Contains(task.Error.Error(), "panicked") {
		t.Errorf("panicky task error should mention panic, got: %v", task.Error)
	}
}
