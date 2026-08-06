package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/config"
)

// withCompactionTimeout must bound the summarize/token-count API calls made
// during pre-emptive compaction so a hung provider endpoint can't stall a turn
// (or a /loop iteration / sub-agent) indefinitely.
func TestWithCompactionTimeout_DefaultApplied(t *testing.T) {
	a := &Agent{} // zero-value field ⇒ default
	ctx, cancel := a.withCompactionTimeout(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline on the compaction context")
	}
	remaining := time.Until(deadline)
	// Compaction is a real model round, so its default must match the configured
	// model-round cap rather than the historical 60-second auxiliary timer.
	if remaining <= config.DefaultModelRoundTimeout-time.Second || remaining > agentCompactionAPITimeout+time.Second {
		t.Fatalf("default deadline = %v from now, want ~%v", remaining, agentCompactionAPITimeout)
	}
}

func TestWithCompactionTimeout_FieldOverride(t *testing.T) {
	a := &Agent{compactionAPITimeout: 25 * time.Millisecond}
	ctx, cancel := a.withCompactionTimeout(context.Background())
	defer cancel()

	// A blocking call that honors ctx must be cut off near the configured bound,
	// not run forever.
	start := time.Now()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("compaction context did not bound the blocking call")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("bound took %v, want ~25ms", elapsed)
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("ctx.Err() = %v, want DeadlineExceeded", ctx.Err())
	}
	if !errors.Is(context.Cause(ctx), client.ErrModelRoundTimeout) {
		t.Fatalf("context cause = %v, want typed model-round timeout", context.Cause(ctx))
	}
}

// The bound must never EXTEND a parent that is already closer to expiry — a
// short-lived parent context wins.
func TestWithCompactionTimeout_HonorsTighterParentDeadline(t *testing.T) {
	a := &Agent{} // default model-round cap
	parent, parentCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer parentCancel()

	ctx, cancel := a.withCompactionTimeout(parent)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if remaining := time.Until(deadline); remaining > time.Second {
		t.Fatalf("derived deadline = %v, want it to honor the ~40ms parent", remaining)
	}
}

func TestWithCompactionTimeoutFollowsLiveModelRoundTimeout(t *testing.T) {
	a := &Agent{}
	a.SetModelRoundTimeout(40 * time.Minute)
	ctx, cancel := a.withCompactionTimeout(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if remaining := time.Until(deadline); remaining < 39*time.Minute || remaining > 40*time.Minute+time.Second {
		t.Fatalf("live compaction deadline = %v, want ~40m", remaining)
	}
}
