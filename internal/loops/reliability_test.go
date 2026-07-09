package loops

import (
	"context"
	"testing"
	"time"
)

// TestTick_SkipsOnCanceledContext pins Fix 1: tick() must check ctx.Err()
// BEFORE scanning/firing. Without this guard, a shutdown race launches a
// doomed fireOne goroutine that immediately returns at its own ctx.Err()
// check — wasting a spawn + startHook + SetFiring/ClearFiring cycle, and
// briefly setting iterationRunning=true so a concurrent tick skips.
func TestTick_SkipsOnCanceledContext(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, err := mgr.Add("test task", ModeSelfPaced, 0)
	if err != nil {
		t.Fatal(err)
	}
	l.NextRunAt = time.Now()
	mgr.loops[l.ID] = l

	spawner := &fakeSpawner{output: "ran", ok: true}
	idle := &fakeIdle{}
	idle.set(true)

	r := NewRunner(mgr, spawner.spawn, idle.check)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r.tick(ctx)

	if spawner.callCount() != 0 {
		t.Errorf("spawn called %d times on canceled ctx; want 0", spawner.callCount())
	}
	if r.iterationRunning.Load() {
		t.Error("iterationRunning = true after tick on canceled ctx; want false")
	}
}

// TestFireOne_SkipsPausedLoop pins Fix 2: if a loop was paused/stopped between
// tick()'s snapshot and fireOne's execution, fireOne must re-check status from
// the authoritative manager and skip the spawn entirely.
func TestFireOne_SkipsPausedLoop(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, err := mgr.Add("test task", ModeSelfPaced, 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &fakeSpawner{output: "ran", ok: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	if err := mgr.Pause(l.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	r.fireOne(context.Background(), l)

	if spawner.callCount() != 0 {
		t.Errorf("spawn called %d times on paused loop; want 0", spawner.callCount())
	}
}

// TestTick_ScanRotationResetsOnEmpty pins Fix 3: when Active() returns an empty
// list, scanRotation must reset to 0 so a newly-created loop's first tick starts
// at index 0.
func TestTick_ScanRotationResetsOnEmpty(t *testing.T) {
	mgr := NewManager(newMemStorage())
	spawner := &fakeSpawner{output: "ok", ok: true}
	idle := &fakeIdle{}
	idle.set(true) // idle MUST be true for tick to proceed
	r := NewRunner(mgr, spawner.spawn, idle.check)
	r.SetPeriod(50 * time.Millisecond)

	r.scanRotation = 5

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.tick(ctx)
	if r.scanRotation != 0 {
		t.Errorf("scanRotation = %d after empty tick; want 0", r.scanRotation)
	}

	l, err := mgr.Add("solo task", ModeSelfPaced, 0)
	if err != nil {
		t.Fatal(err)
	}
	l.NextRunAt = time.Now()
	mgr.loops[l.ID] = l

	r.tick(ctx)

	// tick() launches fireOne in a goroutine, so the spawn may not have
	// completed yet. Wait up to 1s for it.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && spawner.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if spawner.callCount() == 0 {
		t.Error("solo loop did not fire on first tick after scanRotation reset")
	}
}

// TestNewManager_StaggersOverdueLoopsOnRestart pins Fix 4: when gokin restarts
// with multiple Running loops whose NextRunAt is in the past, NewManager must
// spread their NextRunAt across one poll period so the first scheduler tick
// doesn't fire all of them at once (thundering herd).
func TestNewManager_StaggersOverdueLoopsOnRestart(t *testing.T) {
	store := newMemStorage()

	past := time.Now().Add(-1 * time.Hour)
	for i := range 3 {
		l := &Loop{
			ID:              "loop-stagger-" + string(rune('a'+i)),
			Task:            "overdue task",
			Mode:            ModeInterval,
			IntervalSeconds: 3600,
			Status:          StatusRunning,
			CreatedAt:       past.Add(time.Duration(i) * time.Second),
			NextRunAt:       past,
		}
		store.loops[l.ID] = l
	}

	mgr := NewManager(store)

	// All 3 overdue loops must have NextRunAt pushed into the future (staggered).
	// We don't check monotonic ordering because memStorage.Load() returns loops
	// in non-deterministic map order — the point is they're ALL spread out, not
	// all fired at once on the first tick.
	beforeLoad := time.Now().Add(-1 * time.Hour) // safely before any stagger
	var nextTimes []time.Time
	for i := range 3 {
		id := "loop-stagger-" + string(rune('a'+i))
		l, ok := mgr.Get(id)
		if !ok {
			t.Fatalf("loop %s not loaded", id)
		}
		if !l.NextRunAt.After(beforeLoad) {
			t.Errorf("loop %s: NextRunAt %v not staggered into the future", id, l.NextRunAt)
		}
		nextTimes = append(nextTimes, l.NextRunAt)
	}

	// Verify the spread: the min and max NextRunAt should differ by at least
	// one staggerStep (DefaultPollPeriod/staggerDivisor = 10s).
	minT, maxT := nextTimes[0], nextTimes[0]
	for _, nt := range nextTimes[1:] {
		if nt.Before(minT) {
			minT = nt
		}
		if nt.After(maxT) {
			maxT = nt
		}
	}
	expectedMinSpread := DefaultPollPeriod / staggerDivisor
	if maxT.Sub(minT) < expectedMinSpread-time.Second {
		t.Errorf("stagger spread = %v, want at least %v", maxT.Sub(minT), expectedMinSpread)
	}
}

// TestNewManager_DoesNotStaggerFutureDueLoops ensures the stagger only applies
// to loops whose NextRunAt is actually in the past.
func TestNewManager_DoesNotStaggerFutureDueLoops(t *testing.T) {
	store := newMemStorage()
	future := time.Now().Add(30 * time.Minute)

	l := &Loop{
		ID:              "loop-future",
		Task:            "future task",
		Mode:            ModeInterval,
		IntervalSeconds: 3600,
		Status:          StatusRunning,
		CreatedAt:       time.Now().Add(-1 * time.Hour),
		NextRunAt:       future,
	}
	store.loops[l.ID] = l

	mgr := NewManager(store)
	got, ok := mgr.Get("loop-future")
	if !ok {
		t.Fatal("loop not loaded")
	}
	if got.NextRunAt.Before(future.Add(-1 * time.Second)) {
		t.Errorf("future-due loop was staggered: NextRunAt %v, original %v", got.NextRunAt, future)
	}
}

// TestFireOne_SkipsRemovedLoop pins the nil-deref fix in fireOne's status
// re-check: Manager.Get returns (nil, false) for a REMOVED loop, and the first
// cut of the guard (`if !ok || current.Status != ...`) evaluated current.Status
// in the log arguments even when !ok — a nil-pointer panic (recovered by the
// tick goroutine's recover, but a lost iteration + error log). The removed case
// must skip cleanly, without panicking and without spawning.
func TestFireOne_SkipsRemovedLoop(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, err := mgr.Add("test task", ModeSelfPaced, 0)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &fakeSpawner{output: "ran", ok: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)

	if err := mgr.Remove(l.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Must not panic (the pre-fix guard nil-derefed current.Status here) and
	// must not spawn.
	r.fireOne(context.Background(), l)

	if spawner.callCount() != 0 {
		t.Errorf("spawn called %d times on removed loop; want 0", spawner.callCount())
	}
}
