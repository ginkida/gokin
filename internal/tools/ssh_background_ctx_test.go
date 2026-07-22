//go:build unix

package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/tasks"
)

// The long-deferred ssh-background ctx bug: executeBackground passed the
// caller's per-tool ctx straight into the task manager, and Task.Start derives
// its exec context from it — so the executor cancelling the tool ctx (which it
// does the moment the result returns) killed the just-started background ssh
// seconds after "Started background SSH task". The bash Start path has carried
// the WithoutCancel detach since it was written; ssh now mirrors it.
func TestSSHBackgroundTaskSurvivesToolContextCancel(t *testing.T) {
	// Fake `ssh` on PATH: never connects anywhere, just sleeps like a live
	// remote command would.
	dir := t.TempDir()
	fake := filepath.Join(dir, "ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr := tasks.NewManager(t.TempDir())
	t.Cleanup(mgr.CancelAll)

	tool := NewSSHTool()
	tool.SetTaskManager(mgr)

	ctx, cancel := context.WithCancel(context.Background())
	res, err := tool.Execute(ctx, map[string]any{
		"host": "localhost", "user": "tester",
		"command": "echo hi", "run_in_background": true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("background start failed: %s / %s", res.Content, res.Error)
	}
	data, _ := res.Data.(map[string]any)
	taskID, _ := data["task_id"].(string)
	if taskID == "" {
		t.Fatalf("no task_id in result data: %#v", res.Data)
	}

	// The executor cancels the per-tool ctx right after the result returns.
	cancel()
	time.Sleep(300 * time.Millisecond)

	task, ok := mgr.Get(taskID)
	if !ok {
		t.Fatal("task vanished from the manager")
	}
	if got := task.GetStatus(); got != tasks.StatusRunning {
		t.Fatalf("background task status = %v after tool-ctx cancel, want still Running (the ctx-kill bug)", got)
	}
	// Explicit stop still works — the task owns its own cancelFunc.
	if err := mgr.Cancel(taskID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}
