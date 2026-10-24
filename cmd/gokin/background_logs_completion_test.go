package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	backgroundstore "gokin/internal/background"
)

func TestBackgroundLogsFollowDrainsAndReturnsTerminalOutcome(t *testing.T) {
	for _, state := range []string{backgroundstore.StateSucceeded, backgroundstore.StateFailed} {
		t.Run(state, func(t *testing.T) {
			store, worker := newBackgroundControlFixture(t)
			path, _ := store.StdoutPath(worker.id)
			if err := os.WriteFile(path, []byte("first\nlast\n"), 0600); err != nil {
				t.Fatal(err)
			}
			code := 0
			if state == backgroundstore.StateFailed {
				code = 1
			}
			if _, err := store.Finish(worker.id, state, code); err != nil {
				t.Fatal(err)
			}
			job, err := store.Load(worker.id)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var out bytes.Buffer
			err = followBackgroundLogs(ctx, &out, io.Discard, store, job)
			if (err == nil) != (code == 0) || ctx.Err() != nil {
				t.Fatalf("follow outcome=%v context=%v state=%s", err, ctx.Err(), state)
			}
			if out.String() != "first\nlast\n" {
				t.Fatalf("undrained log: %q", out.String())
			}
		})
	}
}

func TestBackgroundLogsZeroLinesPrintsEntireLog(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store, err := backgroundstore.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	id := backgroundstore.NewJobID()
	if err := store.Create(backgroundstore.Job{ID: id, State: backgroundstore.StateStarting, WorkDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	path, _ := store.StdoutPath(id)
	data := strings.Repeat("complete line\n", 350000) // Beyond both the line and byte tail limits.
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newBackgroundLogsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{id, "-n", "0"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != data {
		t.Fatalf("got %d bytes, want all %d", out.Len(), len(data))
	}
}
