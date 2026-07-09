package loops

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// tick() guard edge cases
// ──────────────────────────────────────────────────────────────────────────────

// TestTick_SkipsWhenIterationRunning pins that tick() must not launch a second
// concurrent iteration while one is already running. The iterationRunning flag
// is the serialization gate — without it, two rapid ticks would fire two LLM
// calls simultaneously, stomping on each other's state.
func TestTick_SkipsWhenIterationRunning(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)
	l.NextRunAt = time.Now()
	mgr.loops[l.ID] = l

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)

	// Simulate a running iteration — tick must see this and bail.
	r.iterationRunning.Store(true)

	r.tick(context.Background())

	if spawner.callCount() != 0 {
		t.Errorf("tick launched iteration while iterationRunning=true; spawn count=%d", spawner.callCount())
	}
}

// TestTick_SkipsWhenBusy pins the isIdle gate: if the app is busy (user typing,
// foreground request in flight), tick must not fire even if loops are due.
// This is the "loops never preempt foreground work" invariant.
func TestTick_SkipsWhenBusy(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)
	l.NextRunAt = time.Now()
	mgr.loops[l.ID] = l

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(false) // app is busy

	r := NewRunner(mgr, spawner.spawn, idle.check)

	r.tick(context.Background())

	if spawner.callCount() != 0 {
		t.Errorf("tick fired while app is busy; spawn count=%d", spawner.callCount())
	}
}

// TestTick_SkipsWhenNoDueLoops ensures tick doesn't fire a loop that isn't due
// yet. A loop with NextRunAt in the future must be skipped.
func TestTick_SkipsWhenNoDueLoops(t *testing.T) {
	mgr := NewManager(newMemStorage())
	_, _ = mgr.Add("task", ModeInterval, 3600) // NextRunAt is 1h in the future

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.tick(context.Background())

	if spawner.callCount() != 0 {
		t.Errorf("tick fired a not-yet-due loop; spawn count=%d", spawner.callCount())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// fireOne() lifecycle edge cases
// ──────────────────────────────────────────────────────────────────────────────

// TestFireOne_SkipsStoppedLoop ensures fireOne re-checks the loop status and
// skips if the loop was stopped (not just paused) between tick and fireOne.
func TestFireOne_SkipsStoppedLoop(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	spawner := &fakeSpawner{output: "ran", ok: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	if err := mgr.Stop(l.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	r.fireOne(context.Background(), l)

	if spawner.callCount() != 0 {
		t.Errorf("spawn called on stopped loop; want 0, got %d", spawner.callCount())
	}
}

// TestFireOne_SkipsCompletedLoop ensures a loop that reached MaxIterations
// (StatusCompleted) is not fired again — it's terminal.
func TestFireOne_SkipsCompletedLoop(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0, WithMaxIterations(1))

	// Run one successful iteration to complete the loop.
	spawner := &fakeSpawner{output: "done", ok: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.fireOne(context.Background(), l)

	got, _ := mgr.Get(l.ID)
	if got.Status != StatusCompleted {
		t.Fatalf("precondition: status=%s, want completed after MaxIterations", got.Status)
	}

	// Reset spawner and try to fire again — must skip.
	spawner2 := &fakeSpawner{output: "should not run", ok: true}
	r2 := NewRunner(mgr, spawner2.spawn, (&fakeIdle{}).check)
	r2.fireOne(context.Background(), got)

	if spawner2.callCount() != 0 {
		t.Errorf("spawn called on completed loop; want 0, got %d", spawner2.callCount())
	}
}

// TestFireOne_SkipsStartHookOnSkip ensures the startHook is NOT called when
// fireOne skips due to a paused/stopped loop. Calling startHook would show a
// phantom "loop firing" status in the TUI for an iteration that never runs.
func TestFireOne_SkipsStartHookOnSkip(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	spawner := &fakeSpawner{output: "ran", ok: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	var startCalled bool
	r.SetIterationStartHook(func(string) { startCalled = true })

	if err := mgr.Pause(l.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	r.fireOne(context.Background(), l)

	if startCalled {
		t.Error("startHook called for a skipped (paused) loop; would show phantom firing status")
	}
}

// TestFireOne_RemovedLoopMidIteration ensures that if a loop is removed while
// its iteration is running, RecordIteration returns ErrLoopGone and fireOne
// exits cleanly without firing doneHook or panicking.
func TestFireOne_RemovedLoopMidIteration(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	var doneCalled bool
	spawner := &fakeSpawner{
		output: "working",
		ok:     true,
		callback: func(prompt string) {
			// Remove the loop WHILE the iteration is running.
			_ = mgr.Remove(l.ID)
		},
	}

	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.SetIterationDoneHook(func(string, Iteration) { doneCalled = true })

	// fireOne blocks until spawn returns, by which point the loop is removed.
	r.fireOne(context.Background(), l)

	if doneCalled {
		t.Error("doneHook must NOT fire when loop was removed mid-iteration")
	}

	// Loop should be gone from the manager.
	if _, ok := mgr.Get(l.ID); ok {
		t.Error("loop should have been removed")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Stagger edge cases
// ──────────────────────────────────────────────────────────────────────────────

// TestNewManager_SingleOverdueLoopGetsStaggered verifies that even a single
// overdue loop is pushed slightly into the future (overdueCount+1 offset), not
// left at NextRunAt==now which would fire immediately on the first tick.
func TestNewManager_SingleOverdueLoopGetsStaggered(t *testing.T) {
	store := newMemStorage()
	past := time.Now().Add(-1 * time.Hour)

	l := &Loop{
		ID:              "loop-single",
		Task:            "overdue",
		Mode:            ModeInterval,
		IntervalSeconds: 3600,
		Status:          StatusRunning,
		CreatedAt:       past,
		NextRunAt:       past,
	}
	store.loops[l.ID] = l

	beforeLoad := time.Now()
	mgr := NewManager(store)

	got, ok := mgr.Get("loop-single")
	if !ok {
		t.Fatal("loop not loaded")
	}

	// The single overdue loop must be pushed at least one staggerStep into
	// the future, not left at 'now'.
	if !got.NextRunAt.After(beforeLoad) {
		t.Errorf("single overdue loop not staggered forward: NextRunAt=%v, beforeLoad=%v",
			got.NextRunAt, beforeLoad)
	}
}

// TestNewManager_MixedOverdueAndFutureDueLoops ensures that in a mixed set,
// only the overdue loops are staggered; future-due loops keep their schedule.
func TestNewManager_MixedOverdueAndFutureDueLoops(t *testing.T) {
	store := newMemStorage()
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(30 * time.Minute)

	overdue := &Loop{
		ID: "loop-overdue-mix", Task: "overdue", Mode: ModeInterval,
		IntervalSeconds: 3600, Status: StatusRunning,
		CreatedAt: past, NextRunAt: past,
	}
	futureLoop := &Loop{
		ID: "loop-future-mix", Task: "future", Mode: ModeInterval,
		IntervalSeconds: 3600, Status: StatusRunning,
		CreatedAt: past, NextRunAt: future,
	}
	store.loops[overdue.ID] = overdue
	store.loops[futureLoop.ID] = futureLoop

	mgr := NewManager(store)

	gotOverdue, _ := mgr.Get("loop-overdue-mix")
	gotFuture, _ := mgr.Get("loop-future-mix")

	// Overdue loop must be pushed forward.
	if !gotOverdue.NextRunAt.After(past.Add(time.Minute)) {
		t.Errorf("overdue loop not staggered: NextRunAt=%v", gotOverdue.NextRunAt)
	}

	// Future loop must be unchanged (within tolerance).
	if gotFuture.NextRunAt.Before(future.Add(-1*time.Second)) ||
		gotFuture.NextRunAt.After(future.Add(time.Second)) {
		t.Errorf("future-due loop was modified: original=%v, now=%v",
			future, gotFuture.NextRunAt)
	}
}

// TestNewManager_PausedLoopsNotStaggered ensures paused loops are NOT touched
// by the stagger logic — they're not going to fire anyway, and rewriting their
// NextRunAt would be confusing if the user later resumes.
func TestNewManager_PausedLoopsNotStaggered(t *testing.T) {
	store := newMemStorage()
	past := time.Now().Add(-1 * time.Hour)

	l := &Loop{
		ID: "loop-paused-stagger", Task: "paused but overdue", Mode: ModeInterval,
		IntervalSeconds: 3600, Status: StatusPaused,
		CreatedAt: past, NextRunAt: past,
	}
	store.loops[l.ID] = l

	mgr := NewManager(store)
	got, _ := mgr.Get("loop-paused-stagger")

	// Paused loop's NextRunAt must be unchanged.
	if !got.NextRunAt.Equal(past) {
		t.Errorf("paused loop's NextRunAt was modified: original=%v, now=%v",
			past, got.NextRunAt)
	}
	if got.Status != StatusPaused {
		t.Errorf("paused loop status changed: %s", got.Status)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// scanRotation fairness
// ──────────────────────────────────────────────────────────────────────────────

// TestTick_ScanRotationAdvancesPastFiredLoop verifies that after firing a loop,
// scanRotation advances PAST it so the next tick starts scanning from the next
// loop. This is the fairness mechanism — without it, a chronically-due loop at
// index 0 would permanently starve all loops after it.
func TestTick_ScanRotationAdvancesPastFiredLoop(t *testing.T) {
	mgr := NewManager(newMemStorage())
	a, _ := mgr.Add("loop A", ModeSelfPaced, 0)
	b, _ := mgr.Add("loop B", ModeSelfPaced, 0)

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)

	// Both due now.
	forceDue := func(id string) {
		mgr.mu.Lock()
		mgr.loops[id].NextRunAt = time.Now().Add(-time.Hour)
		mgr.mu.Unlock()
	}

	forceDue(a.ID)
	forceDue(b.ID)

	// First tick should fire loop A (index 0) and advance rotation to 1.
	r.tick(context.Background())

	// Wait for spawn to complete.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && spawner.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if spawner.callCount() == 0 {
		t.Fatal("first tick did not fire any loop")
	}

	if r.scanRotation == 0 {
		t.Error("scanRotation did not advance past fired loop; would starve later loops")
	}
}

// TestTick_MultipleDueLoopsFireOnePerTick verifies the one-per-tick invariant:
// with N due loops, a single tick fires exactly ONE, not all of them.
func TestTick_MultipleDueLoopsFireOnePerTick(t *testing.T) {
	mgr := NewManager(newMemStorage())
	for range 4 {
		l, _ := mgr.Add("due task", ModeSelfPaced, 0)
		l.NextRunAt = time.Now().Add(-time.Hour)
		mgr.loops[l.ID] = l
	}

	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)

	r.tick(context.Background())

	// Wait briefly for the goroutine to spawn.
	time.Sleep(100 * time.Millisecond)

	if spawner.callCount() > 1 {
		t.Errorf("one-per-tick violated: %d spawns in a single tick", spawner.callCount())
	}
	if spawner.callCount() == 0 {
		t.Error("expected exactly 1 spawn, got 0")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Iteration timeout edge case
// ──────────────────────────────────────────────────────────────────────────────

// TestFireOne_IterationTimeoutRecordsFailure verifies that when an iteration
// exceeds its timeout, it's recorded as a failed iteration (not silently
// dropped). The iteration's OWN timeout is distinct from parent cancellation —
// it SHOULD record as a normal failure.
func TestFireOne_IterationTimeoutRecordsFailure(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	// Spawner that blocks longer than the iteration timeout.
	spawner := &fakeSpawner{
		output: "slow",
		ok:     true,
		delay:  200 * time.Millisecond,
	}

	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.SetIterationTimeout(50 * time.Millisecond) // very short timeout

	r.fireOne(context.Background(), l)

	got, _ := mgr.Get(l.ID)
	if got.IterationCount != 1 {
		t.Fatalf("iteration not recorded: IterationCount=%d, want 1", got.IterationCount)
	}
	if got.Iterations[0].OK {
		t.Error("timed-out iteration marked OK; want false")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Concurrent access safety
// ──────────────────────────────────────────────────────────────────────────────

// TestManager_ConcurrentGetAndTransition ensures Get (RLock) and transition
// (Lock) don't race. Run with -race to catch issues.
func TestManager_ConcurrentGetAndTransition(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("task", ModeSelfPaced, 0)

	done := make(chan struct{})

	// Reader: continuously Get.
	go func() {
		defer close(done)
		for range 1000 {
			got, ok := mgr.Get(l.ID)
			if !ok {
				t.Errorf("Get failed during concurrent transition")
				return
			}
			_ = got.Status
		}
	}()

	// Writer: continuously Pause/Resume.
	for range 1000 {
		_ = mgr.Pause(l.ID)
		_ = mgr.Resume(l.ID)
	}

	<-done
}

// TestRunner_ConcurrentTickAndRemove ensures that removing a loop concurrently
// with tick() doesn't cause a panic or race. tick() takes a snapshot via
// Active(), but fireOne re-checks via Get — both must be safe under -race.
// This is a race/panic regression guard: the assertion is "survived 100 ticks
// + concurrent removes without -race tripping or panicking".
func TestRunner_ConcurrentTickAndRemove(t *testing.T) {
	mgr := NewManager(newMemStorage())
	ids := make([]string, 10)
	for i := range ids {
		l, _ := mgr.Add("concurrent task", ModeSelfPaced, 0)
		l.NextRunAt = time.Now().Add(-time.Hour)
		mgr.loops[l.ID] = l
		ids[i] = l.ID
	}

	spawner := &fakeSpawner{output: "ok", ok: true, delay: 10 * time.Millisecond}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)

	tickDone := make(chan struct{})
	go func() {
		defer close(tickDone)
		defer func() {
			if rv := recover(); rv != nil {
				t.Errorf("tick panicked during concurrent remove: %v", rv)
			}
		}()
		for range 100 {
			r.tick(context.Background())
			time.Sleep(time.Millisecond)
		}
	}()

	// Remove loops concurrently.
	for _, id := range ids {
		_ = mgr.Remove(id)
	}

	<-tickDone

	// All loops should be gone from the manager.
	for i, id := range ids {
		if _, ok := mgr.Get(id); ok {
			t.Errorf("loop %d (%s) still present after Remove", i, id)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Compile-time guard
// ──────────────────────────────────────────────────────────────────────────────

var _ atomic.Bool
var _ = errors.Is
