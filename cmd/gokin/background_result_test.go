package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	backgroundstore "gokin/internal/background"
)

func TestBackgroundResultPrintsOnlyLastResult(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	path, _ := store.StdoutPath(worker.id)
	last := `{"type":"result","status":"success","result":"final answer","verification":{"status":"denied"},"incomplete":true}`
	data := "{\"type\":\"result\",\"result\":\"first turn\"}\n{\"type\":\"tool_end\",\"content\":\"" + strings.Repeat("x", 70000) + "\"}\n" + last + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(worker.id, backgroundstore.StateSucceeded, 0); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		cmd := newBackgroundResultCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(io.Discard)
		args := []string{worker.id[:8]}
		want := "final answer\n"
		if asJSON {
			args = append(args, "--json")
			want = last + "\n"
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil || out.String() != want {
			t.Fatalf("json=%v out=%q err=%v", asJSON, out.String(), err)
		}
	}
}

func TestBackgroundResultDistinguishesRunningMissingAndFailed(t *testing.T) {
	store, worker := newBackgroundControlFixture(t)
	lease, err := store.AcquireWorkerLease(worker.id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	run := func(id string) (int, string) {
		cmd := newBackgroundResultCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{id, "--json"})
		err := cmd.Execute()
		if err == nil {
			return 0, ""
		}
		return commandExitCode(err), err.Error()
	}
	if code, msg := run(worker.id); code != 2 || !strings.Contains(msg, "still running") {
		t.Fatalf("running: code=%d message=%s", code, msg)
	}
	if code, msg := run(backgroundstore.NewJobID()); code != 1 || !strings.Contains(msg, "gokin agents") {
		t.Fatalf("missing: code=%d message=%s", code, msg)
	}
	path, _ := store.StdoutPath(worker.id)
	record, _ := json.Marshal(map[string]any{"type": "result", "status": "timeout", "result": "partial answer"})
	if err := os.WriteFile(path, record, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(worker.id, backgroundstore.StateFailed, 1); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(worker.id); code != 1 {
		t.Fatalf("failed job: exit code %d", code)
	}
	if err := os.WriteFile(path, []byte("{\"type\":\"system\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, msg := run(worker.id); code != 1 || !strings.Contains(msg, "without a result") {
		t.Fatalf("missing result: code=%d message=%s", code, msg)
	}
}
