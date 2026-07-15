package agent

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinator_ConcurrentStartCompletesEmptyBatchImmediately(t *testing.T) {
	c := NewCoordinator(nil, nil, &CoordinatorConfig{MaxParallel: 1})
	t.Cleanup(c.Stop)
	allComplete := make(chan struct{}, 1)
	var callbackCount atomic.Int32
	c.SetCallbacks(nil, nil, func(results map[string]*AgentResult) {
		if len(results) != 0 {
			t.Errorf("empty completion results = %+v", results)
		}
		callbackCount.Add(1)
		allComplete <- struct{}{}
	})

	const starters = 100
	var wg sync.WaitGroup
	wg.Add(starters)
	for i := 0; i < starters; i++ {
		go func() {
			defer wg.Done()
			c.Start()
		}()
	}
	wg.Wait()
	results, err := c.WaitWithTimeoutCtx(nil, time.Second)
	if err != nil || len(results) != 0 {
		t.Fatalf("empty batch wait results=%+v err=%v", results, err)
	}
	select {
	case <-allComplete:
	case <-time.After(time.Second):
		t.Fatal("empty batch did not invoke all-complete callback")
	}
	if got := callbackCount.Load(); got != 1 {
		t.Fatalf("all-complete callbacks = %d, want 1", got)
	}
}

func TestCoordinator_NilRunnerFailsTasksWithoutStrandingDependents(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 2})
	t.Cleanup(c.Stop)
	rootID := c.AddTask("root", AgentTypeExplore, PriorityHigh, nil)
	dependentID := c.AddTask("dependent", AgentTypeExplore, PriorityNormal, []string{rootID})
	started := make(chan string, 2)
	completed := make(chan string, 2)
	allComplete := make(chan struct{}, 1)
	c.SetCallbacks(
		func(task *CoordinatedTask) { started <- task.ID },
		func(task *CoordinatedTask, result *AgentResult) {
			valid := result != nil && result.Status == AgentStatusFailed && result.Completed
			switch task.ID {
			case rootID:
				valid = valid && strings.Contains(result.Error, "runner is not configured")
			case dependentID:
				valid = valid && strings.Contains(result.Error, "dependency") && strings.Contains(result.Error, rootID)
			default:
				valid = false
			}
			if !valid {
				t.Errorf("spawn failure result for %s = %+v", task.ID, result)
			}
			completed <- task.ID
		},
		func(map[string]*AgentResult) { allComplete <- struct{}{} },
	)
	c.Start()
	results, err := c.WaitWithTimeout(time.Second)
	if err != nil {
		t.Fatalf("WaitWithTimeout: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want root and dependent", results)
	}
	for _, id := range []string{rootID, dependentID} {
		result := results[id]
		if result == nil || result.Status != AgentStatusFailed || !result.Completed {
			t.Errorf("result %s = %+v", id, result)
		}
		task := c.GetTask(id)
		if task == nil || task.Status != TaskStatusFailed {
			t.Errorf("task %s = %+v, want failed", id, task)
		}
	}
	select {
	case id := <-started:
		t.Fatalf("onStart fired for task without an agent: %s", id)
	default:
	}
	seen := make(map[string]bool)
	for i := 0; i < 2; i++ {
		select {
		case id := <-completed:
			seen[id] = true
		case <-time.After(time.Second):
			t.Fatal("missing task completion callback")
		}
	}
	if !seen[rootID] || !seen[dependentID] {
		t.Fatalf("completion callbacks = %+v", seen)
	}
	select {
	case <-allComplete:
	case <-time.After(time.Second):
		t.Fatal("missing all-complete callback")
	}
}

func TestCoordinator_SpawnPanicBecomesTerminalFailure(t *testing.T) {
	// A zero-value Runner reaches SpawnAsync but cannot publish into its nil
	// agent/result maps, providing a deterministic panic at the integration
	// boundary without adding a test-only hook to production code.
	c := NewCoordinator(context.Background(), &Runner{}, &CoordinatorConfig{MaxParallel: 1})
	t.Cleanup(c.Stop)
	taskID := c.AddTask("panic at spawn", AgentTypeExplore, PriorityNormal, nil)
	c.Start()
	results, err := c.WaitWithTimeout(time.Second)
	if err != nil {
		t.Fatalf("WaitWithTimeout: %v", err)
	}
	result := results[taskID]
	if result == nil || result.Status != AgentStatusFailed || !result.Completed ||
		!strings.Contains(result.Error, "spawn panicked") {
		t.Fatalf("spawn panic result = %+v", result)
	}
	if task := c.GetTask(taskID); task == nil || task.Status != TaskStatusFailed {
		t.Fatalf("task after spawn panic = %+v", task)
	}
}
