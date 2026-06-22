package loops

import (
	"context"
	"testing"
)

// A shutdown / parent-context cancellation during an in-flight iteration must
// NOT be recorded as a failed iteration: doing so pollutes the consecutive-
// failure streak (toward auto-pause) and persists a meaningless "context
// canceled" failure to disk that survives to the next startup. Same class as
// the end-of-turn-gate cancellation fixes — a cancellation is not a failure.

// Parent ctx canceled BEFORE the iteration starts: the spawner is never called
// and nothing is recorded.
func TestFireOne_ShutdownBeforeStartSkips(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 3600)
	spawner := &fakeSpawner{output: "x", ok: false}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.fireOne(ctx, l)

	if spawner.callCount() != 0 {
		t.Errorf("spawner must not be called when parent ctx already canceled, got %d calls", spawner.callCount())
	}
	got, _ := mgr.Get(l.ID)
	if got.IterationCount != 0 || got.FailureCount != 0 || got.ConsecutiveFailures != 0 {
		t.Errorf("nothing should be recorded: iters=%d fails=%d streak=%d",
			got.IterationCount, got.FailureCount, got.ConsecutiveFailures)
	}
}

// Parent ctx canceled DURING the iteration (the realistic shutdown race): the
// spawner runs (and would return a failure), but the post-spawn guard skips the
// record so the failure streak stays clean.
func TestFireOne_ShutdownDuringIterationSkipsRecord(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 3600)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel mid-spawn — fakeSpawner fires the callback before returning.
	spawner := &fakeSpawner{output: "partial work, then killed", ok: false, callback: func(string) { cancel() }}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	r.fireOne(ctx, l)

	if spawner.callCount() != 1 {
		t.Fatalf("spawner should have run once, got %d", spawner.callCount())
	}
	got, _ := mgr.Get(l.ID)
	if got.IterationCount != 0 {
		t.Errorf("shutdown-interrupted iteration must not be recorded, got IterationCount=%d", got.IterationCount)
	}
	if got.FailureCount != 0 || got.ConsecutiveFailures != 0 {
		t.Errorf("shutdown must not increment the failure streak: fails=%d streak=%d",
			got.FailureCount, got.ConsecutiveFailures)
	}
	if len(got.Iterations) != 0 {
		t.Errorf("no iteration entry should be persisted, got %d", len(got.Iterations))
	}
}

// Boundary: a NORMAL failure (no cancellation) is still recorded as before —
// the skip must not swallow genuine failures.
func TestFireOne_NormalFailureStillRecorded(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 3600)
	spawner := &fakeSpawner{output: "could not finish", ok: false}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	r.fireOne(context.Background(), l)

	got, _ := mgr.Get(l.ID)
	if got.IterationCount != 1 || got.FailureCount != 1 || got.ConsecutiveFailures != 1 {
		t.Errorf("a genuine (non-canceled) failure must still record: iters=%d fails=%d streak=%d",
			got.IterationCount, got.FailureCount, got.ConsecutiveFailures)
	}
}
