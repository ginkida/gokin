package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestWaitWithTimeoutCtx_CallerCancelUnblocks pins the coordinate-Esc fix:
// the wait must ALSO select on the CALLER's ctx (the turn ctx Esc cancels) —
// previously it selected only on completion/timer/the coordinator's own
// app-lifetime ctx, so a coordinate turn was un-interruptible by any user
// action (the /loop CancelInFlight bug class, one layer up).
func TestWaitWithTimeoutCtx_CallerCancelUnblocks(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 3})
	defer c.Stop()

	callerCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.WaitWithTimeoutCtx(callerCtx, 10*time.Minute)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel() // the user's Esc

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("caller cancel must surface as a cancellation error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WaitWithTimeoutCtx did not unblock on caller ctx cancel — Esc would hang the turn")
	}
}

// TestCancelRunning_NoRunningIsNoOp guards the unconditional teardown call:
// with nothing running it must be a harmless zero.
func TestCancelRunning_NoRunningIsNoOp(t *testing.T) {
	c := NewCoordinator(context.Background(), nil, &CoordinatorConfig{MaxParallel: 3})
	defer c.Stop()
	if n := c.CancelRunning(); n != 0 {
		t.Fatalf("CancelRunning with no running tasks must return 0, got %d", n)
	}
}
