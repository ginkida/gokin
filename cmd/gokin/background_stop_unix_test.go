//go:build !windows

package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
	"time"

	backgroundstore "gokin/internal/background"
)

func TestBackgroundStopCommandSignalsOnlyLeaseOwnedWorker(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	id := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{
		ID: id, State: backgroundstore.StateStarting,
		WorkDir: t.TempDir(), StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireWorkerLease(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	process := exec.Command("sh", "-c", "sleep 30")
	configureDetachedProcess(process)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = process.Process.Kill()
		_, _ = process.Process.Wait()
	}()
	if _, err := store.MarkRunning(id, process.Process.Pid); err != nil {
		t.Fatal(err)
	}

	command := newBackgroundStopCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{id[:8]})
	if err := command.Execute(); err != nil {
		t.Fatalf("stop command: %v", err)
	}
	if !strings.Contains(out.String(), "Stopping background session") {
		t.Fatalf("stop output = %q", out.String())
	}

	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("detached process group did not stop")
	}
	_ = lease.Release()
	job, err := store.Reconcile(mustResolveBackgroundJob(t, store, id))
	if err != nil {
		t.Fatal(err)
	}
	if job.State != backgroundstore.StateStopped {
		t.Fatalf("reconciled state = %q, want stopped", job.State)
	}
}

func mustResolveBackgroundJob(t *testing.T, store *backgroundstore.Store, id string) backgroundstore.Job {
	t.Helper()
	job, err := store.Resolve(id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
