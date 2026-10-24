package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	backgroundstore "gokin/internal/background"
)

func TestBackgroundLogsFollowDuringMetadataReplacement(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	lease, err := store.AcquireWorkerLease(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	path, _ := store.StdoutPath(worker.id)
	const first = "{\"type\":\"system\"}\n"
	const result = "{\"type\":\"result\",\"status\":\"success\",\"result\":\"done\"}\n"
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	job, err := store.Load(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	var writes atomic.Int64
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(2 * time.Second)
		for ctx.Err() == nil && time.Now().Before(deadline) {
			if _, err := store.SetSessionID(worker.id, "concurrent-session"); err != nil {
				done <- err
				return
			}
			writes.Add(1)
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			_, err = file.WriteString(result)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err == nil {
			_, err = store.Finish(worker.id, backgroundstore.StateSucceeded, 0)
		}
		done <- err
	}()
	var out bytes.Buffer
	followErr := followBackgroundLogs(ctx, &out, io.Discard, store, job)
	if err := <-done; err != nil {
		t.Errorf("writer failed: %v", err)
	}
	if followErr != nil || ctx.Err() != nil || writes.Load() == 0 {
		t.Fatalf("follow failed during atomic metadata replacement: err=%v context=%v writes=%d", followErr, ctx.Err(), writes.Load())
	}
	if got := out.String(); got != first+result {
		t.Fatalf("follow did not drain the final result: %q", got)
	}
	command := newBackgroundResultCmd()
	command.SetArgs([]string{worker.id, "--json"})
	out.Reset()
	command.SetOut(&out)
	command.SetErr(io.Discard)
	if err := command.ExecuteContext(ctx); err != nil || out.String() != result {
		t.Fatalf("result after concurrent updates: %q err=%v", out.String(), err)
	}
	t.Logf("follow drained terminal success after %d metadata replacements", writes.Load())
}
