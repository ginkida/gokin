package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// TestSpawn_RegistersCancelFunc pins the "task_stop lies" fix: the synchronous
// Spawn path never registered the agent's cancel func, so Runner.Cancel
// flipped the STATUS to cancelled (task_stop reported "stopped:true") while
// the run itself kept executing until its own timeout. Spawn must register
// SetCancelFunc — asserted white-box mid-run (an e2e cancel race is untestable
// through MockClient per the documented clone-queue harness mismatch).
func TestSpawn_RegistersCancelFunc(t *testing.T) {
	mock := testkit.NewMockClient()
	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())

	registered := make(chan bool, 1)
	runner.SetOnSubAgentActivity(func(id, typ, task, tool string, args map[string]any, status string, ok bool, summary string) {
		if status != "start" {
			return
		}
		// The start event fires after SetCancelFunc in the fixed code path —
		// snapshot whether the agent has a live cancel func.
		runner.mu.RLock()
		ag := runner.agents[id]
		runner.mu.RUnlock()
		if ag == nil {
			registered <- false
			return
		}
		ag.stateMu.RLock()
		has := ag.cancelFunc != nil
		ag.stateMu.RUnlock()
		registered <- has
	})

	done := make(chan struct{})
	go func() {
		_, _ = runner.Spawn(context.Background(), "general", "quick task", 1, "")
		close(done)
	}()

	select {
	case has := <-registered:
		if !has {
			t.Fatal("Spawn must register the run cancel func BEFORE running — otherwise Runner.Cancel/task_stop only flips status while the run continues")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("agent never started")
	}
	<-done
}
