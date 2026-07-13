package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestCoordinatorTaskSnapshotsDoNotAliasInternalState(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	internal := &CoordinatedTask{
		ID:           "task",
		Prompt:       "original prompt",
		Status:       TaskStatusCompleted,
		Dependencies: []string{"original-dependency"},
		Result: &AgentResult{
			Status: AgentStatusCompleted, Completed: true,
			TouchedPaths: []string{"original.go"},
			Metadata:     map[string]any{"value": "original"},
		},
	}
	c.tasks[internal.ID] = internal

	snapshot := c.GetTask(internal.ID)
	snapshot.Prompt = "mutated prompt"
	snapshot.Dependencies[0] = "mutated-dependency"
	snapshot.Result.Status = AgentStatusFailed
	snapshot.Result.TouchedPaths[0] = "mutated.go"
	snapshot.Result.Metadata["value"] = "mutated"

	fresh := c.GetTask(internal.ID)
	if fresh.Prompt != "original prompt" || fresh.Dependencies[0] != "original-dependency" {
		t.Fatalf("task snapshot mutation reached coordinator state: %+v", fresh)
	}
	if fresh.Result.Status != AgentStatusCompleted || fresh.Result.TouchedPaths[0] != "original.go" || fresh.Result.Metadata["value"] != "original" {
		t.Fatalf("result snapshot mutation reached coordinator state: %+v", fresh.Result)
	}
}

func TestCoordinatorTaskSnapshot_ConcurrentMutationIsRaceFree(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	internal := &CoordinatedTask{
		ID: "task", Status: TaskStatusRunning, Dependencies: []string{"dependency-0"},
		Result: &AgentResult{
			Status: AgentStatusRunning, TouchedPaths: []string{"file-0"},
			Metadata: map[string]any{"counter": 0},
		},
	}
	c.tasks[internal.ID] = internal

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			c.mu.Lock()
			internal.Prompt = fmt.Sprintf("prompt-%d", i)
			internal.Dependencies[0] = fmt.Sprintf("dependency-%d", i)
			internal.Result.Error = fmt.Sprintf("error-%d", i)
			internal.Result.TouchedPaths[0] = fmt.Sprintf("file-%d", i)
			internal.Result.Metadata["counter"] = i
			c.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tasks := c.GetAllTasks()
			if len(tasks) != 1 {
				t.Errorf("task snapshot count = %d, want 1", len(tasks))
				return
			}
			task := tasks[0]
			_ = task.Prompt
			_ = task.Dependencies[0]
			_ = task.Result.Error
			_ = task.Result.TouchedPaths[0]
			_ = task.Result.Metadata["counter"]
		}
	}()
	wg.Wait()
}

func TestCoordinatorCallbacksReceiveOwnedSnapshots(t *testing.T) {
	task := &CoordinatedTask{
		ID: "task", Prompt: "original", Dependencies: []string{"dependency"},
		Result: &AgentResult{Status: AgentStatusCompleted, Metadata: map[string]any{"value": "original"}},
	}
	invokeCoordinatorTaskComplete(func(callbackTask *CoordinatedTask, callbackResult *AgentResult) {
		callbackTask.Prompt = "mutated"
		callbackTask.Dependencies[0] = "mutated"
		callbackTask.Result.Metadata["value"] = "mutated"
		callbackResult.Status = AgentStatusFailed
	}, task, task.Result)

	if task.Prompt != "original" || task.Dependencies[0] != "dependency" || task.Result.Metadata["value"] != "original" || task.Result.Status != AgentStatusCompleted {
		t.Fatalf("callback mutation reached coordinator-owned state: %+v", task)
	}
}
