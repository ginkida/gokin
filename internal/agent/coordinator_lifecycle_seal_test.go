package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoordinatorRejectsTaskAddedAfterStart(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	t.Cleanup(c.Stop)
	c.Start()
	if taskID := c.AddTask("too late", AgentTypeGeneral, PriorityNormal, nil); taskID != "" {
		t.Fatalf("late AddTask returned %q, want explicit rejection", taskID)
	}
	results, err := c.WaitWithTimeout(time.Second)
	if err != nil {
		t.Fatalf("empty sealed coordinator did not finish: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("rejected task leaked into results: %+v", results)
	}
}

func TestCoordinatorUnknownDependencyFailsExplicitly(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	t.Cleanup(c.Stop)
	const missing = "missing-task"
	taskID := c.AddTask("blocked forever before fix", AgentTypeGeneral, PriorityNormal, []string{missing})
	task := c.GetTask(taskID)
	if task == nil || task.Status != TaskStatusFailed || task.Result == nil ||
		!strings.Contains(task.Result.Error, missing) || task.Result.Metadata["dependency_status"] != "missing" {
		t.Fatalf("unknown dependency did not become an explicit terminal result: %+v", task)
	}
	c.Start()
	if _, err := c.WaitWithTimeout(time.Second); err != nil {
		t.Fatalf("unknown dependency stranded Wait: %v", err)
	}
}

func TestCoordinatorAllCompleteFollowsReentrantTaskCallbacks(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	rootID := c.AddTask("root", AgentTypeGeneral, PriorityHigh, nil)
	dependentID := c.AddTask("dependent", AgentTypeGeneral, PriorityNormal, []string{rootID})
	independentID := c.AddTask("independent", AgentTypeGeneral, PriorityLow, nil)

	// Seal synchronously so the test can exercise re-entrant cancellation
	// without racing the process goroutine into nil-runner spawn failures.
	c.mu.Lock()
	c.sealed = true
	c.mu.Unlock()

	var mu sync.Mutex
	var order []string
	c.SetCallbacks(nil, func(task *CoordinatedTask, _ *AgentResult) {
		mu.Lock()
		order = append(order, task.ID)
		mu.Unlock()
		if task.ID == rootID {
			if err := c.CancelTask(independentID); err != nil {
				t.Errorf("re-entrant CancelTask: %v", err)
			}
		}
	}, func(map[string]*AgentResult) {
		mu.Lock()
		order = append(order, "all")
		mu.Unlock()
	})

	if err := c.CancelTask(rootID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.completionCh:
	case <-time.After(time.Second):
		t.Fatal("all-complete was not published")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 4 || order[len(order)-1] != "all" {
		t.Fatalf("callback order = %v; all-complete must follow all task callbacks", order)
	}
	want := map[string]bool{rootID: true, dependentID: true, independentID: true}
	for _, event := range order[:len(order)-1] {
		delete(want, event)
	}
	if len(want) != 0 {
		t.Fatalf("missing task callbacks before all-complete: %v (order %v)", want, order)
	}
}
