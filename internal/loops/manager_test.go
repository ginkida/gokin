package loops

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// memStorage is an in-memory Storage for tests — no disk IO, no
// race-detector noise from filesystem timing.
type memStorage struct {
	loops    map[string]*Loop
	saves    int
	loadErr  []error
	failSave error // when non-nil, Save returns this without persisting
}

func newMemStorage() *memStorage { return &memStorage{loops: make(map[string]*Loop)} }

func (m *memStorage) Load() ([]*Loop, []error) {
	out := make([]*Loop, 0, len(m.loops))
	for _, l := range m.loops {
		c := *l
		out = append(out, &c)
	}
	return out, m.loadErr
}

func (m *memStorage) Save(l *Loop) error {
	if m.failSave != nil {
		return m.failSave
	}
	c := *l
	m.loops[l.ID] = &c
	m.saves++
	return nil
}

func (m *memStorage) Delete(id string) error {
	delete(m.loops, id)
	return nil
}

func TestManager_Add(t *testing.T) {
	m := NewManager(newMemStorage())
	l, err := m.Add("Refactor user service", ModeInterval, 600)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if l.ID == "" {
		t.Error("Add returned loop without ID")
	}
	if l.Status != StatusRunning {
		t.Errorf("new loop status = %s, want running", l.Status)
	}
	if l.UpdateMemory != true {
		t.Error("new loop should have UpdateMemory=true by default")
	}
	if l.MinDelaySeconds != DefaultMinDelaySeconds {
		t.Errorf("new loop MinDelaySeconds = %d, want default %d",
			l.MinDelaySeconds, DefaultMinDelaySeconds)
	}
}

func TestManager_AddOptions(t *testing.T) {
	m := NewManager(newMemStorage())
	l, err := m.Add("Watch CI status", ModeSelfPaced, 0,
		WithMaxIterations(5), WithMinDelay(60), WithoutMemory())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if l.MaxIterations != 5 {
		t.Errorf("MaxIterations = %d, want 5", l.MaxIterations)
	}
	if l.MinDelaySeconds != 60 {
		t.Errorf("MinDelaySeconds = %d, want 60", l.MinDelaySeconds)
	}
	if l.UpdateMemory {
		t.Error("WithoutMemory option ignored")
	}
}

func TestManager_AddRejectsInvalidTask(t *testing.T) {
	m := NewManager(newMemStorage())
	if _, err := m.Add("", ModeInterval, 600); err == nil {
		t.Error("Add accepted empty task")
	}
	if _, err := m.Add("   ", ModeInterval, 600); err == nil {
		t.Error("Add accepted whitespace-only task")
	}
}

func TestManager_AddRejectsZeroInterval(t *testing.T) {
	m := NewManager(newMemStorage())
	if _, err := m.Add("task", ModeInterval, 0); err == nil {
		t.Error("Add accepted interval mode with 0 seconds")
	}
}

func TestManager_GetReturnsClone(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 600)

	got, ok := m.Get(l.ID)
	if !ok {
		t.Fatal("Get missed just-added loop")
	}

	// Mutating the returned clone must not affect manager state.
	got.Status = StatusStopped
	got.Task = "mutated"

	again, _ := m.Get(l.ID)
	if again.Status != StatusRunning {
		t.Error("Get returned shared pointer, not clone")
	}
	if again.Task != "task" {
		t.Error("Task mutation leaked through Get clone")
	}
}

func TestManager_List_StableSort(t *testing.T) {
	m := NewManager(newMemStorage())
	a, _ := m.Add("first", ModeInterval, 60)
	time.Sleep(10 * time.Millisecond)
	b, _ := m.Add("second", ModeInterval, 60)
	time.Sleep(10 * time.Millisecond)
	c, _ := m.Add("third", ModeInterval, 60)

	got := m.List()
	if len(got) != 3 {
		t.Fatalf("List returned %d loops, want 3", len(got))
	}
	if got[0].ID != a.ID || got[1].ID != b.ID || got[2].ID != c.ID {
		t.Errorf("List not in chronological order: got [%s, %s, %s]",
			got[0].ID, got[1].ID, got[2].ID)
	}
}

func TestManager_Active_HidesNonRunning(t *testing.T) {
	m := NewManager(newMemStorage())
	a, _ := m.Add("running", ModeInterval, 60)
	b, _ := m.Add("paused", ModeInterval, 60)
	c, _ := m.Add("stopped", ModeInterval, 60)
	if err := m.Pause(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(c.ID); err != nil {
		t.Fatal(err)
	}

	active := m.Active()
	if len(active) != 1 {
		t.Fatalf("Active should return 1 loop, got %d", len(active))
	}
	if active[0].ID != a.ID {
		t.Errorf("Active returned wrong loop: %s", active[0].ID)
	}
}

func TestManager_PauseResume(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeSelfPaced, 0)

	if err := m.Pause(l.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	got, _ := m.Get(l.ID)
	if got.Status != StatusPaused {
		t.Errorf("after Pause status = %s, want paused", got.Status)
	}

	// Pause when already paused — should error (defensive: either way is valid,
	// pin the current behavior).
	if err := m.Pause(l.ID); err == nil {
		t.Error("Pause on already-paused should error")
	}

	if err := m.Resume(l.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, _ = m.Get(l.ID)
	if got.Status != StatusRunning {
		t.Errorf("after Resume status = %s, want running", got.Status)
	}
}

func TestManager_StopIdempotent(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 60)
	if err := m.Stop(l.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Second stop should be a no-op, not an error.
	if err := m.Stop(l.ID); err != nil {
		t.Errorf("Stop on already-stopped should be no-op, got: %v", err)
	}
}

func TestManager_FireNow(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 3600) // 1h interval — won't fire naturally for a while

	if err := m.FireNow(l.ID); err != nil {
		t.Fatalf("FireNow: %v", err)
	}
	got, _ := m.Get(l.ID)
	if !got.IsDue(time.Now()) {
		t.Error("after FireNow, loop should be due")
	}
}

func TestManager_FireNowOnPausedErrors(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 60)
	if err := m.Pause(l.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.FireNow(l.ID); err == nil {
		t.Error("FireNow on paused loop should error")
	}
}

func TestManager_Remove(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 60)

	if err := m.Remove(l.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := m.Get(l.ID); ok {
		t.Error("Remove didn't drop loop from manager state")
	}
	if err := m.Remove(l.ID); err == nil {
		t.Error("Remove on already-removed should error (not idempotent at manager level — distinct from storage idempotence)")
	}
}

func TestManager_RecordIteration(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 600)

	now := time.Now()
	err := m.RecordIteration(l.ID, Iteration{
		N: 1, StartedAt: now, Duration: 5 * time.Second,
		Summary: "did the thing", OK: true,
	})
	if err != nil {
		t.Fatalf("RecordIteration: %v", err)
	}

	got, _ := m.Get(l.ID)
	if got.IterationCount != 1 {
		t.Errorf("IterationCount = %d, want 1", got.IterationCount)
	}
	if len(got.Iterations) != 1 {
		t.Errorf("len(Iterations) = %d, want 1", len(got.Iterations))
	}
	if !strings.Contains(got.Iterations[0].Summary, "did the thing") {
		t.Errorf("summary lost in roundtrip: %q", got.Iterations[0].Summary)
	}
}

// TestManager_LoadCorruptFilesLogs: corrupt files at startup are
// surfaced via logging but don't block manager construction.
func TestManager_LoadCorruptFilesLogs(t *testing.T) {
	s := newMemStorage()
	s.loadErr = []error{errCorruptForTest}
	// Just ensure NewManager doesn't panic when load returns errors.
	_ = NewManager(s)
}

var errCorruptForTest = corruptErr("synthetic corrupt file")

type corruptErr string

func (e corruptErr) Error() string { return string(e) }

// TestManager_PersistsAcrossNew: the manager loads existing loops on
// construction, so a fresh process picks up where the prior left off.
// This is the core of the "loops survive restart" guarantee.
func TestManager_PersistsAcrossNew(t *testing.T) {
	s := newMemStorage()

	// Pretend a prior process saved a loop.
	original := &Loop{
		ID:              "loop-prior",
		Task:            "Continue from before",
		Mode:            ModeInterval,
		IntervalSeconds: 600,
		Status:          StatusRunning,
		CreatedAt:       time.Now(),
	}
	if err := s.Save(original); err != nil {
		t.Fatal(err)
	}

	// New process starts up.
	m := NewManager(s)
	got, ok := m.Get("loop-prior")
	if !ok {
		t.Fatal("Manager didn't load existing loop")
	}
	if got.Task != "Continue from before" {
		t.Errorf("Task = %q, want %q", got.Task, "Continue from before")
	}
}

// TestManager_RecordIteration_GoneAfterRemove: a long-running iteration
// finishes and tries to RecordIteration after the user has /loop
// removed the loop. RecordIteration must return ErrLoopGone (not a
// generic "not found" error) so the runner can skip the doneHook
// instead of writing an orphan markdown file for the just-deleted loop.
func TestManager_RecordIteration_GoneAfterRemove(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 600)

	if err := m.Remove(l.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	err := m.RecordIteration(l.ID, Iteration{N: 1, Summary: "done"})
	if !errors.Is(err, ErrLoopGone) {
		t.Errorf("RecordIteration after Remove: err = %v, want ErrLoopGone", err)
	}
}

// TestManager_RecordIteration_NotRunning: a long-running iteration
// finishes and tries to RecordIteration after the user has paused the
// loop. RecordIteration must return ErrLoopNotRunning so the runner
// skips downstream side effects but the loop record itself stays
// intact (different from removed — the user wants to /loop resume
// later).
func TestManager_RecordIteration_NotRunning(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 600)

	if err := m.Pause(l.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	err := m.RecordIteration(l.ID, Iteration{N: 1, Summary: "done"})
	if !errors.Is(err, ErrLoopNotRunning) {
		t.Errorf("RecordIteration on paused loop: err = %v, want ErrLoopNotRunning", err)
	}

	// The loop should still exist with the user's pause intact.
	got, ok := m.Get(l.ID)
	if !ok {
		t.Fatal("loop disappeared after rejected RecordIteration")
	}
	if got.Status != StatusPaused {
		t.Errorf("loop status = %s, want paused", got.Status)
	}
	if len(got.Iterations) != 0 {
		t.Errorf("paused loop got %d iterations recorded, want 0", len(got.Iterations))
	}
}

// TestManager_RecordIteration_RollsBackOnSaveError: same divergence
// guard as transition(), specialized to the RecordIteration path.
// Without rollback, AppendIteration's mutations to IterationCount,
// LastRunAt, NextRunAt, and Iterations would survive in-memory while
// the disk file remained at the pre-call state — next restart would
// "lose" the iteration count, but in-session N keeps incrementing,
// silently desynchronizing from disk truth.
func TestManager_RecordIteration_RollsBackOnSaveError(t *testing.T) {
	stor := newMemStorage()
	m := NewManager(stor)
	l, _ := m.Add("task", ModeInterval, 600)

	// Sanity: clean state before the failed record.
	got, _ := m.Get(l.ID)
	if got.IterationCount != 0 {
		t.Fatalf("baseline IterationCount = %d, want 0", got.IterationCount)
	}

	stor.failSave = errors.New("disk full")
	err := m.RecordIteration(l.ID, Iteration{N: 1, Summary: "done"})
	if err == nil {
		t.Fatal("RecordIteration with failing Save: expected error, got nil")
	}

	got, _ = m.Get(l.ID)
	if got.IterationCount != 0 {
		t.Errorf("after failed record, IterationCount = %d, want 0 (rollback)",
			got.IterationCount)
	}
	if len(got.Iterations) != 0 {
		t.Errorf("after failed record, Iterations len = %d, want 0 (rollback)",
			len(got.Iterations))
	}
}

// TestManager_TransitionRollsBackOnSaveError: when Storage.Save fails
// after a successful mutate, the in-memory loop must be reverted to
// its pre-mutation state. Otherwise callers see "I paused the loop"
// in memory but a restart would show the loop still running, because
// the disk never got the new state. The rollback closes that
// divergence.
func TestManager_TransitionRollsBackOnSaveError(t *testing.T) {
	stor := newMemStorage()
	m := NewManager(stor)
	l, _ := m.Add("task", ModeInterval, 600)

	// Inject failure on the next Save.
	stor.failSave = errors.New("disk full")

	err := m.Pause(l.ID)
	if err == nil {
		t.Fatal("Pause: expected error, got nil")
	}

	got, _ := m.Get(l.ID)
	if got.Status != StatusRunning {
		t.Errorf("after failed save, in-memory status = %s, want still running (rollback)",
			got.Status)
	}
}

// TestManager_PauseResumeRecomputesNextRunAt: a loop paused for hours
// shouldn't fire N times immediately on resume to "catch up". Resume
// recomputes NextRunAt from "now".
func TestManager_PauseResumeRecomputesNextRunAt(t *testing.T) {
	m := NewManager(newMemStorage())
	l, _ := m.Add("task", ModeInterval, 600)

	// Force NextRunAt into the past (simulates "paused for hours").
	if err := m.transition(l.ID, func(loop *Loop) error {
		loop.NextRunAt = time.Now().Add(-2 * time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.Pause(l.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Resume(l.ID); err != nil {
		t.Fatal(err)
	}

	got, _ := m.Get(l.ID)
	// After resume, NextRunAt should be ~10 minutes from now (not in the past).
	if got.NextRunAt.Before(time.Now()) {
		t.Errorf("Resume left NextRunAt in the past: %v", got.NextRunAt)
	}
}
