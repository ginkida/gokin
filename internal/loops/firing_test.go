package loops

import (
	"context"
	"testing"
	"time"
)

func TestManager_FiringState(t *testing.T) {
	m := NewManager(newMemStorage())
	if _, _, ok := m.FiringState(); ok {
		t.Fatal("FiringState should report not-firing initially")
	}
	now := time.Now()
	m.SetFiring("loop-1", now)
	id, since, ok := m.FiringState()
	if !ok || id != "loop-1" || !since.Equal(now) {
		t.Fatalf("FiringState = (%q, %v, %v), want (loop-1, %v, true)", id, since, ok, now)
	}
	m.ClearFiring()
	if _, _, ok := m.FiringState(); ok {
		t.Error("FiringState should be cleared after ClearFiring")
	}
}

// The Runner must mark the loop firing FOR THE DURATION of the iteration and
// clear it on return, so /loop status/list can show "running now".
func TestFireOne_MarksFiringDuringIteration(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 3600)
	var firingDuringSpawn bool
	spawner := &fakeSpawner{output: "ok", ok: true, callback: func(string) {
		id, _, ok := mgr.FiringState()
		firingDuringSpawn = ok && id == l.ID
	}}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.fireOne(context.Background(), l)

	if !firingDuringSpawn {
		t.Error("FiringState should report the loop while its iteration is executing")
	}
	if _, _, ok := mgr.FiringState(); ok {
		t.Error("FiringState should be cleared after fireOne returns")
	}
}

// The mid-streak transient warning fires when the streak == the warn threshold;
// pin that the threshold sits below the backstop and the loop is still Running
// there (so the warning is a heads-up, not a pause).
func TestTransientWarnThreshold_BelowBackstopAndStillRunning(t *testing.T) {
	if TransientFailureWarnThreshold >= TransientFailureLimit {
		t.Fatalf("warn threshold (%d) must be below the backstop (%d)", TransientFailureWarnThreshold, TransientFailureLimit)
	}
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	for i := 0; i < TransientFailureWarnThreshold; i++ {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: time.Now(), OK: false, Transient: true})
	}
	if l.Status != StatusRunning {
		t.Fatalf("loop should still be Running at the warn threshold; status=%s", l.Status)
	}
	if l.ConsecutiveTransientFailures != TransientFailureWarnThreshold {
		t.Errorf("streak = %d, want %d (the warning fires when == this)", l.ConsecutiveTransientFailures, TransientFailureWarnThreshold)
	}
}
