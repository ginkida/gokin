package poll

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitReadyReturnsWhenProbeReady(t *testing.T) {
	if err := WaitReady(context.Background(), func() bool { return true }); err != nil {
		t.Fatalf("WaitReady with ready probe returned %v, want nil", err)
	}
}

func TestWaitReadyHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- WaitReady(ctx, func() bool { return false })
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitReady returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitReady did not return promptly after context cancellation")
	}
}
