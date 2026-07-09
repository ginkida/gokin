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

	c.Start() // processLoop only runs after an explicit Start (coordinate.go does the same)
	c.AddTask("say hi", AgentTypeGeneral, 5, nil)

	select {
	case n := <-reentered:
		if n == 0 {
			t.Error("GetAllTasks inside onTaskStart returned 0 tasks")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("onTaskStart never completed — invoked under c.mu (re-entrant GetAllTasks deadlocked)")
	}
}
