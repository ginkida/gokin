package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/tools"
)

// TestProcessReadyTasks_OnTaskStartFiresOutsideLock pins the round-9
// coordinator fix: onTaskStart used to be invoked from startTask WHILE
// processReadyTasks held c.mu — a callback that re-enters the coordinator
// (the agent-tree snapshot calls GetAllTasks → c.mu.RLock) self-deadlocked
// the scheduling goroutine. The latent bug never detonated only because the
// sole wired coordinator (the boot instance) never received tasks; wiring the
// coordinate tool's per-call coordinators (this round) made the path live.
func TestProcessReadyTasks_OnTaskStartFiresOutsideLock(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewCoordinator(ctx, runner, &CoordinatorConfig{MaxParallel: 1})
	defer c.Stop()

	reentered := make(chan int, 1)
	c.SetCallbacks(
		func(task *CoordinatedTask) {
			// Re-enter the coordinator exactly like the agent-tree snapshot
			// does. Pre-fix this blocks forever on c.mu (held by
			// processReadyTasks) and the test times out.
			reentered <- len(c.GetAllTasks())
		},
		nil, nil,
	)

	c.AddTask("say hi", AgentTypeGeneral, 5, nil)
	c.Start() // Start seals the pre-built graph, then launches processLoop.

	select {
	case n := <-reentered:
		if n == 0 {
			t.Error("GetAllTasks inside onTaskStart returned 0 tasks")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("onTaskStart never completed — invoked under c.mu (re-entrant GetAllTasks deadlocked)")
	}

	// Drain: wait for the spawned task to reach a terminal state before the
	// test returns. The coordinator marks a task Completed/Failed only after
	// the runner's spawn goroutine recorded the result — i.e. after agent.Run
	// returned and every write under workDir/.gokin (the agent-output .log,
	// saved agent state) has happened. Returning earlier races t.TempDir()'s
	// RemoveAll against the agent's file creation, which flaked as
	// "TempDir RemoveAll cleanup: … .gokin/agent-output: directory not empty".
	// GetStatus snapshots all counters under c.mu.RLock, so polling it is
	// race-detector-safe (unlike reading Status off GetAllTasks' live
	// pointers).
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := c.GetStatus()
		if st.PendingTasks == 0 && st.BlockedTasks == 0 && st.ReadyTasks == 0 && st.RunningTasks == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spawned task never reached a terminal state (%+v) — returning would race TempDir cleanup", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
