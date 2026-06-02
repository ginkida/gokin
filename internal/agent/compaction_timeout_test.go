package agent

import (
	"context"
	"testing"
	"time"
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
	// Default is 60s; allow generous slack for scheduling.
	if remaining <= 50*time.Second || remaining > agentCompactionAPITimeout+time.Second {
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
}

// The bound must never EXTEND a parent that is already closer to expiry — a
// short-lived parent context wins.
func TestWithCompactionTimeout_HonorsTighterParentDeadline(t *testing.T) {
	a := &Agent{} // default 60s
	parent, parentCancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer parentCancel()

	ctx, cancel := a.withCompactionTimeout(parent)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline")
	}
	if remaining := time.Until(deadline); remaining > time.Second {
		t.Fatalf("derived deadline = %v, want it to honor the ~40ms parent, not extend to 60s", remaining)
	}
}
