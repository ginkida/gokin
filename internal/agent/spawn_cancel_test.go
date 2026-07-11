package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/client"
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

// TestSpawnMultiple_RegistersCancelFunc mirrors TestSpawn_RegistersCancelFunc
// for the fan-out primitive (used by SpawnMultiple's callers, incl. /audit's
// find/verify phases). SpawnMultiple fires no "start" activity event (unlike
// Spawn), so this polls the registered agent directly — the mock's stream
// delay keeps the worker inside its model call long enough for the poll to
// reliably observe cancelFunc set.
func TestSpawnMultiple_RegistersCancelFunc(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.EnqueueScript(testkit.ResponseScript{
		Chunks:                []client.ResponseChunk{{Text: "done", Done: true}},
		DelayBeforeFirstChunk: 2 * time.Second,
	})
	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())

	done := make(chan struct{})
	go func() {
		_, _ = runner.SpawnMultiple(context.Background(), []AgentTask{
			{Type: AgentTypeGeneral, Prompt: "quick task", MaxTurns: 1},
		})
		close(done)
	}()

	deadline := time.Now().Add(4 * time.Second)
	var found, has bool
	for time.Now().Before(deadline) {
		runner.mu.RLock()
		for _, ag := range runner.agents {
			ag.stateMu.RLock()
			if ag.cancelFunc != nil {
				has = true
			}
			ag.stateMu.RUnlock()
			found = true
		}
		runner.mu.RUnlock()
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !found {
		t.Fatal("SpawnMultiple never registered the agent")
	}
	if !has {
		t.Fatal("SpawnMultiple must register the run cancel func — otherwise Runner.Cancel/task_stop only flips status while the run continues")
	}
	<-done
}
