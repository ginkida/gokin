package tools

import (
	"context"
	"testing"
	"time"

	"gokin/internal/tasks"
	"gokin/internal/testkit"
)

// Deferred finding from the v0.100.88 subsystem audit, verified here: cloning
// a BashTool for a sub-agent handed it a BRAND-NEW tasks.Manager, so a
// sub-agent's run_in_background task was write-only — invisible to /tasks (the
// foreground manager), unreadable by task_output and unstoppable by kill_shell
// (both are unclonable singletons pointing at the foreground manager), and
// never cancelled at shutdown because nothing tracked the private manager.
// One manager owns every task; the per-clone working directory travels with
// the task instead.
func TestClonedBashToolSharesTaskManagerButKeepsWorkDir(t *testing.T) {
	// A non-isolated sub-agent clones the bash tool for the SAME project dir
	// (an isolated apply-back clone refuses run_in_background outright, so the
	// split-brain only ever bit this — the common — shape).
	foreground := testkit.ResolvedTempDir(t)
	agentDir := foreground

	mgr := tasks.NewManager(foreground)
	t.Cleanup(mgr.CancelAll)

	bash := NewBashTool(foreground)
	bash.SetTaskManager(mgr)

	cloned, ok := CloneToolForWorkDir(bash, agentDir).(*BashTool)
	if !ok {
		t.Fatal("clone did not produce a *BashTool")
	}

	res, err := cloned.Execute(context.Background(), map[string]any{
		"command":           "sleep 5",
		"run_in_background": true,
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

	task, found := mgr.Get(taskID)
	if !found {
		t.Fatal("sub-agent background task is invisible to the foreground manager (split-brain)")
	}
	if task.WorkDir != agentDir {
		t.Fatalf("task WorkDir = %q, want the CLONE's dir %q — sharing the manager must not move the task's cwd", task.WorkDir, agentDir)
	}

	// Stoppable through the same manager that /tasks and kill_shell use.
	if err := mgr.Cancel(taskID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := task.GetStatus(); s != tasks.StatusRunning {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("cancelled sub-agent task never left Running")
}
