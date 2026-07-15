package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestWaitWithTimeout_TimeoutSurfacesPartialResults pins the fix: when the
// deadline fires with SOME tasks already finished, WaitWithTimeout must
// return those completed AgentResults instead of nil — checkCompletedAgents
// populates task.Result as each agent finishes, independent of whether all
// tasks are done, so a 4-of-5 partial completion is real data sitting in
// c.tasks at the moment the timer fires. Before the fix, both the timer and
// ctx-cancellation branches returned (nil, err), discarding every completed
// sub-task's work whenever the whole batch didn't finish in time.
func TestWaitWithTimeout_TimeoutSurfacesPartialResults(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 3})

	c.mu.Lock()
	c.tasks["t-done"] = &CoordinatedTask{
		ID: "t-done", Status: TaskStatusCompleted,
		Result: &AgentResult{AgentID: "a1", Output: "built the thing", Completed: true},
	}
	c.tasks["t-running"] = &CoordinatedTask{
		ID: "t-running", Status: TaskStatusRunning,
		// No Result yet — still in flight when the deadline hits.
	}
	c.mu.Unlock()

	results, err := c.WaitWithTimeout(10 * time.Millisecond)

	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if results == nil {
		t.Fatal("expected a partial results map, got nil — completed work was discarded")
	}
	got, ok := results["t-done"]
	if !ok {
		t.Fatal("completed task's result missing from the partial snapshot")
	}
	if got.Output != "built the thing" {
		t.Errorf("got Output=%q, want the completed task's real output", got.Output)
	}
	if _, ok := results["t-running"]; ok {
		t.Error("the still-running task must NOT appear in the partial results (it has no Result yet)")
	}
}

// TestWaitWithTimeout_CtxCancelSurfacesPartialResults: same fix, the
// ctx.Done() branch.
func TestWaitWithTimeout_CtxCancelSurfacesPartialResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := NewCoordinator(ctx, nil, &CoordinatorConfig{MaxParallel: 3})

	c.mu.Lock()
	c.tasks["t-done"] = &CoordinatedTask{
		ID: "t-done", Status: TaskStatusCompleted,
		Result: &AgentResult{AgentID: "a1", Output: "finished before cancel", Completed: true},
	}
	c.mu.Unlock()

	cancel() // cancel immediately so WaitWithTimeout's ctx.Done() branch fires first

	results, err := c.WaitWithTimeout(time.Minute)

	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if results == nil {
		t.Fatal("expected a partial results map on cancellation, got nil")
	}
	if got, ok := results["t-done"]; !ok || got.Output != "finished before cancel" {
		t.Errorf("completed task's result missing/wrong on cancellation path: %+v (ok=%v)", got, ok)
	}
}

// TestWaitWithTimeout_NormalCompletionUnaffected: the fix must not change
// the happy path — all tasks done, no error.
func TestWaitWithTimeout_NormalCompletionUnaffected(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 3})

	c.mu.Lock()
	c.sealed = true
	c.tasks["t1"] = &CoordinatedTask{ID: "t1", Status: TaskStatusCompleted, Result: &AgentResult{Output: "ok"}}
	c.mu.Unlock()

	// Drive the durable completion signal the way processLoop does once every
	// task is done. WaitWithTimeout may begin before or after this notification.
	go func() {
		time.Sleep(20 * time.Millisecond)
		c.notifyAllComplete()
	}()

	results, err := c.WaitWithTimeout(2 * time.Second)
	if err != nil {
		t.Fatalf("unexpected error on normal completion: %v", err)
	}
	if got, ok := results["t1"]; !ok || got.Output != "ok" {
		t.Errorf("normal completion results wrong: %+v", results)
	}
}
