package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func completedCoordinatorForWaitTests() *Coordinator {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	c.sealed = true
	c.tasks["task-done"] = &CoordinatedTask{
		ID:     "task-done",
		Status: TaskStatusCompleted,
		Result: &AgentResult{Output: "done", Completed: true},
	}
	return c
}

func TestCoordinatorWait_AfterCompletionReturnsImmediately(t *testing.T) {
	c := completedCoordinatorForWaitTests()
	c.notifyAllComplete()

	done := make(chan map[string]*AgentResult, 1)
	go func() { done <- c.Wait() }()

	select {
	case results := <-done:
		if got := results["task-done"]; got == nil || got.Output != "done" {
			t.Fatalf("late waiter received wrong results: %+v", results)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait blocked after completion was already notified")
	}
}

func TestCoordinatorWait_MultipleConcurrentWaitersAllReturn(t *testing.T) {
	c := completedCoordinatorForWaitTests()

	const waiterCount = 8
	start := make(chan struct{})
	errs := make(chan string, waiterCount)
	var wg sync.WaitGroup
	for i := 0; i < waiterCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results, err := c.WaitWithTimeout(time.Second)
			if err != nil {
				errs <- err.Error()
				return
			}
			if got := results["task-done"]; got == nil || got.Output != "done" {
				errs <- "wrong result"
			}
		}()
	}

	close(start)
	c.notifyAllComplete()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent waiter failed: %s", err)
	}
}

func TestCoordinatorWait_DoesNotReplaceAllCompleteCallback(t *testing.T) {
	c := completedCoordinatorForWaitTests()
	var callbackCalls atomic.Int32
	c.SetCallbacks(nil, nil, func(results map[string]*AgentResult) {
		if got := results["task-done"]; got != nil && got.Output == "done" {
			callbackCalls.Add(1)
		}
	})

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		_, _ = c.WaitWithTimeout(time.Second)
	}()
	c.notifyAllComplete()

	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("waiter did not receive completion")
	}
	if got := callbackCalls.Load(); got != 1 {
		t.Fatalf("all-complete callback calls = %d, want 1", got)
	}
}
