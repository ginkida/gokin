package agent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCoordinatorTaskCompleteCallbackCanWaitWithoutEarlyAllComplete(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 1})
	t.Cleanup(c.Stop)
	taskID := c.AddTask("finish locally", AgentTypeGeneral, PriorityNormal, nil)

	// Seal without starting the process loop: cancelling this pending task is a
	// deterministic final state transition performed by the test goroutine.
	c.mu.Lock()
	c.sealed = true
	c.mu.Unlock()

	callbackWaitReturned := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackReturned := make(chan struct{})
	allComplete := make(chan struct{})
	callbackErr := make(chan error, 1)
	c.SetCallbacks(nil, func(task *CoordinatedTask, _ *AgentResult) {
		results := c.Wait()
		if task == nil || task.ID != taskID {
			callbackErr <- fmt.Errorf("onTaskComplete task = %+v, want %s", task, taskID)
		} else if result := results[taskID]; result == nil || result.Status != AgentStatusCancelled {
			callbackErr <- fmt.Errorf("Wait result = %+v, want cancelled task", results)
		}
		close(callbackWaitReturned)
		<-releaseCallback
		close(callbackReturned)
	}, func(map[string]*AgentResult) {
		close(allComplete)
	})

	cancelReturned := make(chan error, 1)
	go func() {
		cancelReturned <- c.CancelTask(taskID)
	}()

	select {
	case <-callbackWaitReturned:
	case err := <-callbackErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("onTaskComplete deadlocked calling Wait")
	}

	// Wait means that the sealed task graph is terminal. It does not mean all
	// observer callbacks have returned; onAllComplete retains that stronger
	// delivery guarantee.
	select {
	case <-allComplete:
		t.Fatal("onAllComplete fired before onTaskComplete returned")
	default:
	}
	select {
	case err := <-cancelReturned:
		t.Fatalf("CancelTask returned before its synchronous callback: %v", err)
	default:
	}

	close(releaseCallback)
	select {
	case <-callbackReturned:
	case <-time.After(time.Second):
		t.Fatal("onTaskComplete did not return after release")
	}
	select {
	case err := <-cancelReturned:
		if err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelTask remained blocked after onTaskComplete returned")
	}
	select {
	case <-allComplete:
	case <-time.After(time.Second):
		t.Fatal("onAllComplete did not follow onTaskComplete")
	}
	select {
	case err := <-callbackErr:
		t.Fatal(err)
	default:
	}
}
