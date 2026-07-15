package agent

import (
	"context"
	"testing"
)

func TestCoordinatorCompletionUsesWaiterSnapshotAfterRunnerEviction(t *testing.T) {
	runner := NewRunner(context.Background(), nil, nil, t.TempDir())
	coordinator := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 1})
	taskID := coordinator.AddTask("inspect", AgentTypeExplore, PriorityNormal, nil)
	agentID := "already-evicted-agent"
	coordinator.mu.Lock()
	coordinator.tasks[taskID].Status = TaskStatusRunning
	coordinator.running[agentID] = taskID
	coordinator.sealed = true
	coordinator.mu.Unlock()

	// This snapshot is what WaitWithContext returned. Runner.results intentionally
	// has no entry, modelling cleanup immediately after waiter consumption.
	result := &AgentResult{
		AgentID:   agentID,
		Status:    AgentStatusCompleted,
		Output:    "finished",
		Completed: true,
	}
	coordinator.handleAgentCompletionResult(agentID, result)

	task := coordinator.GetTask(taskID)
	if task == nil || task.Status != TaskStatusCompleted || task.Result == nil || task.Result.Output != "finished" {
		t.Fatalf("coordinator lost waiter-owned result after runner eviction: %+v", task)
	}
	if _, stillRunning := coordinator.running[agentID]; stillRunning {
		t.Fatal("completed agent remained in coordinator running map")
	}
}
