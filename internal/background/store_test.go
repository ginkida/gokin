package background

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreLifecycleAndPrivateFiles(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	workDir := t.TempDir()
	job := Job{
		ID:        id,
		State:     StateStarting,
		WorkDir:   workDir,
		StartedAt: time.Now(),
	}
	if err := store.Create(job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	path, _ := store.jobPath(id)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("job mode = %o, want 600", info.Mode().Perm())
	}

	lease, err := store.AcquireWorkerLease(id)
	if err != nil {
		t.Fatalf("AcquireWorkerLease: %v", err)
	}
	defer lease.Release()
	if held, err := store.WorkerLeaseHeld(id); err != nil || !held {
		t.Fatalf("WorkerLeaseHeld = %v, %v", held, err)
	}
	if _, err := store.MarkRunning(id, os.Getpid()); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := store.SetSessionID(id, "session-1"); err != nil {
		t.Fatalf("SetSessionID: %v", err)
	}
	running, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != StateRunning || running.PID != os.Getpid() || running.SessionID != "session-1" {
		t.Fatalf("running job = %+v", running)
	}
	if _, err := store.Finish(id, StateSucceeded, 0); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	finished, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != StateSucceeded || finished.EndedAt.IsZero() {
		t.Fatalf("finished job = %+v", finished)
	}
}

func TestStoreCreateValidatesJobLineage(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	base := Job{
		ID:          id,
		ParentJobID: "not-a-job-id",
		State:       StateStarting,
		WorkDir:     t.TempDir(),
	}
	if err := store.Create(base); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("invalid parent Create() error = %v", err)
	}
	base.ParentJobID = id
	if err := store.Create(base); err == nil || !strings.Contains(err.Error(), "own parent") {
		t.Fatalf("self-parent Create() error = %v", err)
	}

	parentID := NewJobID()
	base.ParentJobID = parentID
	if err := store.Create(base); err != nil {
		t.Fatalf("valid lineage Create(): %v", err)
	}
	got, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentJobID != parentID {
		t.Fatalf("ParentJobID = %q, want %q", got.ParentJobID, parentID)
	}
}

func TestStoreReconcileInterruptedAndStopped(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		state string
		want  string
	}{
		{state: StateRunning, want: StateInterrupted},
		{state: StateStopping, want: StateStopped},
	} {
		id := NewJobID()
		if err := store.Create(Job{
			ID:        id,
			State:     StateStarting,
			WorkDir:   t.TempDir(),
			StartedAt: time.Now().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(id, func(job *Job) error {
			job.State = tc.state
			job.PID = 999999
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		reconciled, err := store.Reconcile(mustLoadJob(t, store, id))
		if err != nil {
			t.Fatalf("Reconcile(%s): %v", tc.state, err)
		}
		if reconciled.State != tc.want {
			t.Fatalf("Reconcile(%s) state = %q, want %q", tc.state, reconciled.State, tc.want)
		}
	}
}

func TestStoreListFiltersWorkspaceAndCompletion(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	firstID := NewJobID()
	secondID := NewJobID()
	for _, job := range []Job{
		{ID: firstID, State: StateStarting, WorkDir: firstDir, StartedAt: time.Now()},
		{ID: secondID, State: StateStarting, WorkDir: secondDir, StartedAt: time.Now().Add(time.Second)},
	} {
		if err := store.Create(job); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Update(firstID, func(job *Job) error {
		job.State = StateSucceeded
		job.EndedAt = time.Now()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	active, err := store.List("", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != secondID {
		t.Fatalf("active jobs = %+v", active)
	}
	filtered, err := store.List(firstDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].ID != firstID {
		t.Fatalf("workspace jobs = %+v", filtered)
	}
}

func TestStoreRejectsUnsafeIdentityAndSymlinkState(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "not-a-uuid", "67C220A6-5BA6-4D36-95BD-2DF9A9F49D94"} {
		if _, err := store.Load(id); err == nil {
			t.Errorf("unsafe ID %q was accepted", id)
		}
	}

	id := NewJobID()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := store.jobPath(id)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.Load(id); err == nil {
		t.Fatal("symlink job state was accepted")
	}
}

func TestStoreResolveAcceptsUniquePrefix(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "67c220a6-5ba6-4d36-95bd-2df9a9f49d94"
	if err := store.Create(Job{
		ID: id, State: StateStarting, WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	job, err := store.Resolve("67c220a6")
	if err != nil || job.ID != id {
		t.Fatalf("Resolve prefix = %+v, %v", job, err)
	}
	if _, err := store.Resolve("../escape"); err == nil {
		t.Fatal("unsafe prefix was accepted")
	}
}

func TestStoreUpdateSerializesConcurrentWriters(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{
		ID: id, State: StateStarting, WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	const writers = 40
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, updateErr := store.Update(id, func(job *Job) error {
				job.PID++
				return nil
			})
			errs <- updateErr
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Update: %v", err)
		}
	}
	job := mustLoadJob(t, store, id)
	if job.PID != writers {
		t.Fatalf("serialized PID counter = %d, want %d", job.PID, writers)
	}
}

func TestControlInboxClaimsInOrderAndFailsClosedOnAmbiguity(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{
		ID: id, State: StateStarting, WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	first, err := store.EnqueueControl(id, "first follow-up")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnqueueControl(id, "second follow-up")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextControl(id)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != first.ID || claimed.Message != first.Message {
		t.Fatalf("first claim = %+v", claimed)
	}
	if _, err := store.ClaimNextControl(id); !errors.Is(err, ErrAmbiguousControl) {
		t.Fatalf("unacknowledged claim did not block overtaking: %v", err)
	}
	counted, err := store.RefreshControlCounts(mustLoadJob(t, store, id))
	if err != nil {
		t.Fatal(err)
	}
	if counted.AmbiguousInput != 1 || counted.PendingInput != 1 {
		t.Fatalf("control counts = pending:%d ambiguous:%d", counted.PendingInput, counted.AmbiguousInput)
	}
	if err := store.CompleteControl(*claimed, "steered"); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimNextControl(id)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != second.ID {
		t.Fatalf("second claim = %+v", claimed)
	}
	if err := store.CompleteControl(*claimed, "next_turn"); err != nil {
		t.Fatal(err)
	}
	if next, err := store.ClaimNextControl(id); err != nil || next != nil {
		t.Fatalf("empty inbox = %+v, %v", next, err)
	}
}

func TestControlInboxValidationAndTerminalGate(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{
		ID: id, State: StateStarting, WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"", " \n", "bad\x00message", strings.Repeat("x", maxControlBytes+1)} {
		if _, err := store.EnqueueControl(id, message); err == nil {
			t.Errorf("invalid control message was accepted (len=%d)", len(message))
		}
	}
	if _, err := store.Finish(id, StateSucceeded, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueControl(id, "too late"); err == nil {
		t.Fatal("terminal job accepted control input")
	}
}

func TestBeginFinishingClosesInboxWithoutStrandingAcceptedInput(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	if err := store.Create(Job{
		ID: id, State: StateStarting, WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueControl(id, "committed first"); err != nil {
		t.Fatal(err)
	}
	if finishing, err := store.BeginFinishing(id); err != nil || finishing {
		t.Fatalf("finish with pending input = %v, %v", finishing, err)
	}
	control, err := store.ClaimNextControl(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteControl(*control, "completed"); err != nil {
		t.Fatal(err)
	}
	if finishing, err := store.BeginFinishing(id); err != nil || !finishing {
		t.Fatalf("finish empty inbox = %v, %v", finishing, err)
	}
	if _, err := store.EnqueueControl(id, "too late"); err == nil {
		t.Fatal("finishing job accepted input")
	}
}

func mustLoadJob(t *testing.T, store *Store, id string) Job {
	t.Helper()
	job, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
