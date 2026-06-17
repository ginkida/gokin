package context

import (
	"context"
	"testing"
	"time"
)

// TestBoundedCompactionCtx pins the /compact hang fix: an unbounded ctx (the
// deadline-less slash-command ctx) gets capped at compactionBudget, while a
// tighter parent deadline is preserved (never extended) so the already-bounded
// background path is unaffected.
func TestBoundedCompactionCtx(t *testing.T) {
	// No deadline → bounded to ~compactionBudget.
	c1, cancel1 := boundedCompactionCtx(context.Background())
	defer cancel1()
	dl1, ok := c1.Deadline()
	if !ok {
		t.Fatal("unbounded ctx must gain a deadline")
	}
	if d := time.Until(dl1); d <= 0 || d > compactionBudget+time.Second {
		t.Errorf("bounded deadline = %v, want ~%v", d, compactionBudget)
	}

	// Tighter parent deadline → preserved, not extended.
	parent, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer pcancel()
	c2, cancel2 := boundedCompactionCtx(parent)
	defer cancel2()
	dl2, ok := c2.Deadline()
	if !ok {
		t.Fatal("ctx should still have the parent deadline")
	}
	if d := time.Until(dl2); d > 3*time.Second {
		t.Errorf("tighter parent deadline must be preserved, got %v", d)
	}
}
