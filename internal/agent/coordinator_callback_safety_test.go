package agent

import (
	"context"
	"testing"

	"gokin/internal/tools"
)

func TestCoordinatorCallbackPanicsAreContained(t *testing.T) {
	task := &CoordinatedTask{ID: "task-panic"}
	result := &AgentResult{AgentID: "agent-panic"}

	assertNoPanic := func(name string, invoke func()) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("callback panic escaped coordinator: %v", recovered)
				}
			}()
			invoke()
		})
	}

	assertNoPanic("task start", func() {
		invokeCoordinatorTaskStart(func(*CoordinatedTask) { panic("start") }, task)
	})
	assertNoPanic("task complete", func() {
		invokeCoordinatorTaskComplete(func(*CoordinatedTask, *AgentResult) { panic("complete") }, task, result)
	})
	assertNoPanic("all complete", func() {
		invokeCoordinatorAllComplete(func(map[string]*AgentResult) { panic("all") }, map[string]*AgentResult{task.ID: result})
	})
}

func TestCheckCompletedAgents_CallbackPanicDoesNotSkipRemainingTasks(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	c := NewCoordinator(context.Background(), runner, &CoordinatorConfig{MaxParallel: 2})

	const taskCount = 2
	for i := 0; i < taskCount; i++ {
		agentID := "agent-" + string(rune('a'+i))
		taskID := "task-" + string(rune('a'+i))
		result := &AgentResult{
			AgentID:   agentID,
			Status:    AgentStatusCompleted,
			Completed: true,
		}

		runner.mu.Lock()
		runner.results[agentID] = result
		runner.mu.Unlock()

		c.mu.Lock()
		c.running[agentID] = taskID
		c.tasks[taskID] = &CoordinatedTask{ID: taskID, Status: TaskStatusRunning}
		c.mu.Unlock()
	}

	callbackCalls := 0
	c.SetCallbacks(nil, func(*CoordinatedTask, *AgentResult) {
		callbackCalls++
		panic("observer failure")
	}, nil)

	c.checkCompletedAgents()

	if callbackCalls != taskCount {
		t.Fatalf("completion callback calls = %d, want %d; one panic skipped a later notification", callbackCalls, taskCount)
	}
	status := c.GetStatus()
	if status.CompletedTasks != taskCount || status.RunningTasks != 0 {
		t.Fatalf("coordinator state after callback panic = %+v", status)
	}
}

func TestNotifyAllComplete_CallbackPanicDoesNotEscape(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	c.sealed = true
	c.SetCallbacks(nil, nil, func(map[string]*AgentResult) {
		panic("observer failure")
	})

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("all-complete callback panic escaped: %v", recovered)
		}
	}()
	c.notifyAllComplete()
}
