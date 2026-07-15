package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/tools"
)

func TestCoordinatorDependencyFailure_CascadesDiamondOnceAndPreservesIndependentBranch(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 4})
	t.Cleanup(c.Stop)

	rootID := c.AddTask("root", AgentTypeGeneral, PriorityHigh, nil)
	leftID := c.AddTask("left", AgentTypeExplore, PriorityNormal, []string{rootID})
	rightID := c.AddTask("right", AgentTypeBash, PriorityNormal, []string{rootID})
	joinID := c.AddTask("join", AgentTypePlan, PriorityNormal, []string{leftID, rightID})
	independentID := c.AddTask("independent", AgentTypeGeneral, PriorityLow, nil)

	var callbackMu sync.Mutex
	callbackCount := make(map[string]int)
	c.SetCallbacks(nil, func(task *CoordinatedTask, result *AgentResult) {
		// Re-entering the coordinator would deadlock if cascade notifications
		// were fired while c.mu was held.
		if snapshot := c.GetTask(task.ID); snapshot == nil || snapshot.Result == nil {
			t.Errorf("callback observed incomplete task snapshot for %s: %+v", task.ID, snapshot)
		}
		callbackMu.Lock()
		callbackCount[task.ID]++
		callbackMu.Unlock()
	}, nil)

	cancelDone := make(chan error, 1)
	go func() { cancelDone <- c.CancelTask(rootID) }()
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelTask deadlocked while delivering dependency-failure callbacks")
	}

	root := c.GetTask(rootID)
	if root == nil || root.Status != TaskStatusFailed || root.Result == nil || root.Result.Status != AgentStatusCancelled {
		t.Fatalf("cancelled root = %+v", root)
	}

	wantDependency := map[string]string{
		leftID:  rootID,
		rightID: rootID,
		joinID:  leftID,
	}
	for taskID, dependencyID := range wantDependency {
		task := c.GetTask(taskID)
		if task == nil || task.Status != TaskStatusFailed || task.Result == nil ||
			task.Result.Status != AgentStatusFailed || !task.Result.Completed {
			t.Fatalf("dependency-failed task %s = %+v", taskID, task)
		}
		if !strings.Contains(task.Result.Error, dependencyID) || !strings.Contains(task.Result.Error, "dependency") {
			t.Errorf("task %s error %q does not identify dependency %s", taskID, task.Result.Error, dependencyID)
		}
		if got := task.Result.Metadata["dependency_id"]; got != dependencyID {
			t.Errorf("task %s dependency metadata = %v, want %s", taskID, got, dependencyID)
		}
	}

	independent := c.GetTask(independentID)
	if independent == nil || independent.Status != TaskStatusReady || independent.Result != nil {
		t.Fatalf("independent branch was affected by failure cascade: %+v", independent)
	}
	ready := c.queue.GetReadyTasks()
	if len(ready) != 1 || ready[0].ID != independentID {
		t.Fatalf("ready queue after cascade = %+v, want only %s", ready, independentID)
	}

	callbackMu.Lock()
	defer callbackMu.Unlock()
	for _, taskID := range []string{rootID, leftID, rightID, joinID} {
		if got := callbackCount[taskID]; got != 1 {
			t.Errorf("completion callbacks for %s = %d, want exactly 1", taskID, got)
		}
	}
	if got := callbackCount[independentID]; got != 0 {
		t.Errorf("independent task received %d completion callbacks", got)
	}
}

func TestCoordinatorDependencyFailure_AgentFailureDoesNotRunDependent(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	c := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 2})
	t.Cleanup(c.Stop)

	rootID := c.AddTask("root", AgentTypeExplore, PriorityHigh, nil)
	dependentID := c.AddTask("dependent", AgentTypeGeneral, PriorityNormal, []string{rootID})
	const agentID = "failed-prerequisite-agent"

	c.mu.Lock()
	c.queue.RemoveTask(rootID)
	c.tasks[rootID].Status = TaskStatusRunning
	c.running[agentID] = rootID
	c.mu.Unlock()
	runner.mu.Lock()
	runner.results[agentID] = &AgentResult{
		AgentID: agentID, Type: AgentTypeExplore, Status: AgentStatusFailed,
		Error: "compile failed", Completed: true,
	}
	runner.mu.Unlock()

	var callbackMu sync.Mutex
	callbackCount := make(map[string]int)
	c.SetCallbacks(nil, func(task *CoordinatedTask, _ *AgentResult) {
		callbackMu.Lock()
		callbackCount[task.ID]++
		callbackMu.Unlock()
	}, nil)

	c.handleAgentCompletion(agentID)

	dependent := c.GetTask(dependentID)
	if dependent == nil || dependent.Status != TaskStatusFailed || dependent.Result == nil ||
		!dependent.Result.Completed || !strings.Contains(dependent.Result.Error, rootID) ||
		!strings.Contains(dependent.Result.Error, "compile failed") {
		t.Fatalf("dependent after prerequisite agent failure = %+v", dependent)
	}
	for _, ready := range c.queue.GetReadyTasks() {
		if ready.ID == dependentID {
			t.Fatalf("failed dependent was enqueued to run: %+v", ready)
		}
	}
	callbackMu.Lock()
	defer callbackMu.Unlock()
	if callbackCount[rootID] != 1 || callbackCount[dependentID] != 1 {
		t.Fatalf("completion callback counts = %+v, want root and dependent once", callbackCount)
	}
}

func TestCoordinatorDependencies_AllSuccessfulUnblockOnlyAfterLastCompletion(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	c := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 2})
	t.Cleanup(c.Stop)

	leftID := c.AddTask("left", AgentTypeExplore, PriorityNormal, nil)
	rightID := c.AddTask("right", AgentTypeExplore, PriorityNormal, nil)
	joinID := c.AddTask("join", AgentTypeGeneral, PriorityHigh, []string{leftID, rightID})
	const leftAgentID = "successful-left-agent"
	const rightAgentID = "successful-right-agent"

	c.mu.Lock()
	c.queue.RemoveTask(leftID)
	c.queue.RemoveTask(rightID)
	c.tasks[leftID].Status = TaskStatusRunning
	c.tasks[rightID].Status = TaskStatusRunning
	c.running[leftAgentID] = leftID
	c.running[rightAgentID] = rightID
	c.mu.Unlock()
	runner.mu.Lock()
	runner.results[leftAgentID] = &AgentResult{AgentID: leftAgentID, Status: AgentStatusCompleted, Completed: true}
	runner.results[rightAgentID] = &AgentResult{AgentID: rightAgentID, Status: AgentStatusCompleted, Completed: true}
	runner.mu.Unlock()

	c.handleAgentCompletion(leftAgentID)
	if task := c.GetTask(joinID); task == nil || task.Status != TaskStatusBlocked {
		t.Fatalf("join after one successful prerequisite = %+v, want blocked", task)
	}

	c.handleAgentCompletion(rightAgentID)
	if task := c.GetTask(joinID); task == nil || task.Status != TaskStatusReady || task.Result != nil {
		t.Fatalf("join after all successful prerequisites = %+v, want ready", task)
	}
	ready := c.queue.GetReadyTasks()
	if len(ready) != 1 || ready[0].ID != joinID {
		t.Fatalf("ready queue = %+v, want only join task", ready)
	}
}
