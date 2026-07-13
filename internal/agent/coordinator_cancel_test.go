package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/tools"
)

// TestWaitWithTimeoutCtx_CallerCancelUnblocks pins the coordinate-Esc fix:
// the wait must ALSO select on the CALLER's ctx (the turn ctx Esc cancels) —
// previously it selected only on completion/timer/the coordinator's own
// app-lifetime ctx, so a coordinate turn was un-interruptible by any user
// action (the /loop CancelInFlight bug class, one layer up).
func TestWaitWithTimeoutCtx_CallerCancelUnblocks(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 3})
	defer c.Stop()

	callerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.WaitWithTimeoutCtx(callerCtx, 10*time.Minute)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel() // the user's Esc

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("caller cancel must surface as a cancellation error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitWithTimeoutCtx did not unblock on caller ctx cancel — Esc would hang the turn")
	}
}

// TestCancelRunning_NoRunningIsNoOp guards the unconditional teardown call:
// with nothing running it must be a harmless zero.
func TestCancelRunning_NoRunningIsNoOp(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 3})
	defer c.Stop()
	if n := c.CancelRunning(); n != 0 {
		t.Fatalf("CancelRunning with no running tasks must return 0, got %d", n)
	}
}

func TestCancelTask_IsTerminalAndUnblocksDependents(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	prerequisiteID := c.AddTask("prerequisite", AgentTypeGeneral, PriorityNormal, nil)
	dependentID := c.AddTask("dependent", AgentTypeGeneral, PriorityNormal, []string{prerequisiteID})

	var completionCalls atomic.Int32
	c.SetCallbacks(nil, func(task *CoordinatedTask, result *AgentResult) {
		if task.ID == prerequisiteID && result.Status == AgentStatusCancelled {
			completionCalls.Add(1)
		}
	}, nil)

	if err := c.CancelTask(prerequisiteID); err != nil {
		t.Fatalf("CancelTask returned error: %v", err)
	}

	c.mu.RLock()
	prerequisite := c.tasks[prerequisiteID]
	dependent := c.tasks[dependentID]
	markedComplete := c.completed[prerequisiteID]
	c.mu.RUnlock()

	if prerequisite.Status != TaskStatusFailed || prerequisite.Result == nil ||
		prerequisite.Result.Status != AgentStatusCancelled || !prerequisite.Result.Completed {
		t.Fatalf("cancelled prerequisite is not a complete terminal failure: %+v", prerequisite)
	}
	if !markedComplete {
		t.Fatal("cancelled task missing from coordinator completed set")
	}
	if dependent.Status != TaskStatusReady {
		t.Fatalf("dependent status = %s, want %s", dependent.Status, TaskStatusReady)
	}
	if got := completionCalls.Load(); got != 1 {
		t.Fatalf("completion callback calls = %d, want 1", got)
	}
}

func TestCancelTask_AllCompleteWakesWaiters(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	taskID := c.AddTask("cancel me", AgentTypeGeneral, PriorityNormal, nil)

	if err := c.CancelTask(taskID); err != nil {
		t.Fatalf("CancelTask returned error: %v", err)
	}

	results, err := c.WaitWithTimeout(time.Second)
	if err != nil {
		t.Fatalf("wait after terminal cancellation returned error: %v", err)
	}
	result := results[taskID]
	if result == nil || result.Status != AgentStatusCancelled || !result.Completed {
		t.Fatalf("wait returned wrong cancellation result: %+v", result)
	}
}

func TestCancelTask_DoesNotOverwriteExistingTerminalResult(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	taskID := c.AddTask("already done", AgentTypeGeneral, PriorityNormal, nil)
	original := &AgentResult{Status: AgentStatusCompleted, Output: "kept", Completed: true}
	c.mu.Lock()
	c.tasks[taskID].Status = TaskStatusCompleted
	c.tasks[taskID].Result = original
	c.completed[taskID] = true
	c.queue.RemoveTask(taskID)
	c.mu.Unlock()

	if err := c.CancelTask(taskID); err != nil {
		t.Fatalf("idempotent CancelTask returned error: %v", err)
	}
	if got := c.GetTask(taskID).Result; got == nil || got.Output != "kept" || got.Status != AgentStatusCompleted {
		t.Fatalf("CancelTask overwrote a terminal result: %+v", got)
	}
}

func TestCancelTask_RunningAgentCompletesExactlyOnce(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	c := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 1})
	taskID := c.AddTask("running", AgentTypeGeneral, PriorityNormal, nil)
	const agentID = "cancel-running-agent"

	runner.mu.Lock()
	runner.agents[agentID] = &Agent{ID: agentID, status: AgentStatusRunning}
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()
	c.mu.Lock()
	c.queue.RemoveTask(taskID)
	c.tasks[taskID].Status = TaskStatusRunning
	c.running[agentID] = taskID
	c.mu.Unlock()

	var completionCalls atomic.Int32
	c.SetCallbacks(nil, func(*CoordinatedTask, *AgentResult) {
		completionCalls.Add(1)
	}, nil)

	if err := c.CancelTask(taskID); err != nil {
		t.Fatalf("CancelTask returned error: %v", err)
	}
	// A delayed monitor notification must be ignored after CancelTask removed
	// the running mapping, rather than publishing a duplicate completion.
	c.handleAgentCompletion(agentID)

	result := c.GetTask(taskID).Result
	if result == nil || result.AgentID != agentID || result.Status != AgentStatusCancelled {
		t.Fatalf("wrong running-task cancellation result: %+v", result)
	}
	if got := completionCalls.Load(); got != 1 {
		t.Fatalf("completion callback calls = %d, want exactly 1", got)
	}
}
