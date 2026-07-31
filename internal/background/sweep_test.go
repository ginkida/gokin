package background

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTerminalJob(t *testing.T, store *Store, id string, endedAt time.Time) {
	t.Helper()
	if err := store.Create(Job{
		ID: id, State: StateStarting, WorkDir: t.TempDir(), StartedAt: endedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(id, func(job *Job) error {
		job.State = StateSucceeded
		job.EndedAt = endedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stdout, err := store.StdoutPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdout, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jobExists(t *testing.T, store *Store, id string) bool {
	t.Helper()
	path, err := store.jobPath(id)
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

// Every detached run used to leave its record, logs, locks and inbox behind
// forever, and past maxJobs entries `gokin agents` failed permanently.
func TestSweepRemovesAgedTerminalJobsAndTheirArtifacts(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := NewJobID()
	recent := NewJobID()
	writeTerminalJob(t, store, old, time.Now().Add(-30*24*time.Hour))
	writeTerminalJob(t, store, recent, time.Now().Add(-time.Hour))

	store.Sweep(7*24*time.Hour, 200)

	if jobExists(t, store, old) {
		t.Fatal("aged terminal job survived the sweep")
	}
	if !jobExists(t, store, recent) {
		t.Fatal("recent terminal job was swept")
	}
	stdout, err := store.StdoutPath(old)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(stdout); !os.IsNotExist(statErr) {
		t.Fatalf("aged job's log survived: %v", statErr)
	}
	if entries, readErr := os.ReadDir(filepath.Join(store.Root(), "locks")); readErr == nil {
		for _, entry := range entries {
			if filepath.Base(entry.Name())[:8] == old[:8] {
				t.Fatalf("aged job left a lock behind: %s", entry.Name())
			}
		}
	}
}

// A live worker's state is not housekeeping's to delete.
func TestSweepNeverRemovesALeasedJob(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewJobID()
	writeTerminalJob(t, store, id, time.Now().Add(-30*24*time.Hour))
	lease, err := store.AcquireWorkerLease(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	store.Sweep(time.Hour, 1)
	if !jobExists(t, store, id) {
		t.Fatal("sweep deleted a job whose worker lease is still held")
	}
}

// Beyond the keep newest, terminal jobs go even when they are recent — that is
// what bounds the directory.
func TestSweepEnforcesTheKeepCeiling(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 5)
	for index := range 5 {
		id := NewJobID()
		writeTerminalJob(t, store, id, time.Now().Add(-time.Duration(index)*time.Minute))
		ids = append(ids, id)
	}

	store.Sweep(0, 2)

	surviving := 0
	for _, id := range ids {
		if jobExists(t, store, id) {
			surviving++
		}
	}
	if surviving != 2 {
		t.Fatalf("surviving jobs = %d, want the 2 newest", surviving)
	}
}
