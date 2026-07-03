package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"gokin/internal/tools"
)

// TestCoordinatorAgentResultRace_CancelVsCheckCompletedAgents (round 4,
// adversarial-review follow-up) pins the HIGH-severity finding: Coordinator's
// checkCompletedAgents/handleAgentCompletion used to call
// c.runner.GetResult(agentID) — which releases r.mu BEFORE returning the
// *AgentResult pointer — and then read result.Completed with NO lock at all.
// Runner.Cancel (reachable independently of Coordinator's c.mu via
// tools/task_stop.go's task_stop tool and app/signals.go's shutdown path —
// NOT only via Coordinator.CancelTask, which DOES take c.mu) mutates
// result.Status/.Completed on the SAME pointer under r.mu.Lock(). This is
// the identical unsynchronized-flag-read bug the round's bonus fix
// (completedResultLocked) closed for Runner.WaitWithContext — left open one
// call away in Coordinator. Fixed by routing both Coordinator methods
// through completedResultLocked instead of GetResult()+a raw field read.
//
// This test races Runner.Cancel (simulating the task_stop/shutdown path,
// which does NOT take c.mu) against a tight loop of
// Coordinator.checkCompletedAgents() calls (simulating the periodic ticker)
// under -race — it must report no data race and must eventually observe the
// task transition to TaskStatusFailed (Cancel sets AgentStatusCancelled,
// which checkCompletedAgents maps to TaskStatusFailed).
func TestCoordinatorAgentResultRace_CancelVsCheckCompletedAgents(t *testing.T) {
	registry := tools.NewRegistry()
	runner := NewRunner(context.Background(), nil, registry, t.TempDir())

	const agentID = "race-agent-1"
	const taskID = "race-task-1"

	agent := &Agent{ID: agentID, status: AgentStatusRunning}

	runner.mu.Lock()
	runner.agents[agentID] = agent
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()

	coordinator := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 3})
	coordinator.mu.Lock()
	coordinator.running[agentID] = taskID
	coordinator.tasks[taskID] = &CoordinatedTask{ID: taskID, Status: TaskStatusRunning}
	coordinator.mu.Unlock()

	stop := make(chan struct{})
	pollerDone := make(chan struct{})
	var cancelWG sync.WaitGroup

	// Goroutine A: simulates task_stop/shutdown calling Runner.Cancel
	// DIRECTLY — bypassing Coordinator's c.mu entirely, exactly the
	// unsynchronized path the finding identified.
	cancelWG.Add(1)
	go func() {
		defer cancelWG.Done()
		_ = runner.Cancel(agentID)
	}()

	// Goroutine B: simulates the periodic checkCompletedAgents ticker,
	// polling concurrently with the cancel above until told to stop.
	go func() {
		defer close(pollerDone)
		for {
			select {
			case <-stop:
				return
			default:
				coordinator.checkCompletedAgents()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	cancelWG.Wait() // Cancel has run; the poller keeps racing below
	// Give checkCompletedAgents a few more ticks to observe the now-completed
	// result, then stop the poller and wait for it to actually exit.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.RLock()
		done := coordinator.completed[taskID]
		coordinator.mu.RUnlock()
		if done {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-pollerDone

	coordinator.mu.RLock()
	task := coordinator.tasks[taskID]
	completed := coordinator.completed[taskID]
	coordinator.mu.RUnlock()

	if !completed {
		t.Fatal("expected checkCompletedAgents to eventually observe the cancelled agent as completed")
	}
	if task.Status != TaskStatusFailed {
		t.Fatalf("task status = %v, want %v (Cancel sets AgentStatusCancelled, which checkCompletedAgents maps to Failed)", task.Status, TaskStatusFailed)
	}
}

// TestCoordinatorAgentResultRace_CancelVsHandleAgentCompletion is the same
// race, exercising handleAgentCompletion (the channel-driven path) instead
// of checkCompletedAgents (the ticker-driven path) — both had the identical
// unguarded read and both needed the fix.
func TestCoordinatorAgentResultRace_CancelVsHandleAgentCompletion(t *testing.T) {
	registry := tools.NewRegistry()
	runner := NewRunner(context.Background(), nil, registry, t.TempDir())

	const agentID = "race-agent-2"
	const taskID = "race-task-2"

	agent := &Agent{ID: agentID, status: AgentStatusRunning}

	runner.mu.Lock()
	runner.agents[agentID] = agent
	runner.results[agentID] = &AgentResult{AgentID: agentID, Status: AgentStatusRunning}
	runner.mu.Unlock()

	coordinator := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 3})
	coordinator.mu.Lock()
	coordinator.running[agentID] = taskID
	coordinator.tasks[taskID] = &CoordinatedTask{ID: taskID, Status: TaskStatusRunning}
	coordinator.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = runner.Cancel(agentID)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			coordinator.handleAgentCompletion(agentID)
			time.Sleep(time.Millisecond)
		}
	}()
	wg.Wait()

	coordinator.mu.RLock()
	task := coordinator.tasks[taskID]
	completed := coordinator.completed[taskID]
	coordinator.mu.RUnlock()

	if !completed {
		t.Fatal("expected handleAgentCompletion to eventually observe the cancelled agent as completed")
	}
	if task.Status != TaskStatusFailed {
		t.Fatalf("task status = %v, want %v", task.Status, TaskStatusFailed)
	}
}
