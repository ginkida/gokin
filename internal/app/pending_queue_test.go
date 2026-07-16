package app

import (
	"fmt"
	"testing"

	"gokin/internal/tools"
)

// TestDrainPendingClearsQueue + TestCancelProcessingDrainsQueue pin that
// cancelling (Esc/Ctrl+C) discards queued type-ahead messages, so a stale queued
// message doesn't start a NEW request right after the user stopped the run.
func TestDrainPendingClearsQueue(t *testing.T) {
	a := &App{}
	for i := 0; i < 3; i++ {
		a.enqueuePending(fmt.Sprintf("m%d", i))
	}
	if n := a.drainPending(); n != 3 {
		t.Errorf("drainPending returned %d, want 3", n)
	}
	if got := a.pendingCount(); got != 0 {
		t.Errorf("queue not empty after drain: %d", got)
	}
	if n := a.drainPending(); n != 0 {
		t.Errorf("drain on empty returned %d, want 0", n)
	}
}

func TestRecoveryQueueKeepsCheckpointLineage(t *testing.T) {
	a := &App{}
	checkpoints := []tools.ToolCheckpoint{{
		CallID: "write-1", ToolName: "write",
		Result: tools.NewSuccessResult("written"),
	}}
	if pos, ok := a.enqueueRecoveryPending("retry", "raw retry", "recovery-1", "session-1", 0, checkpoints); !ok || pos != 1 {
		t.Fatalf("enqueue recovery: pos=%d ok=%v", pos, ok)
	}
	// The queue owns its own slice even if the scheduling closure is reused.
	checkpoints[0].CallID = "mutated-by-caller"

	request, remaining, ok := a.dequeuePendingRequest()
	if !ok || remaining != 0 || request.message != "retry" {
		t.Fatalf("dequeue recovery = %+v remaining=%d ok=%v", request, remaining, ok)
	}
	if len(request.recoveryCheckpoints) != 1 || request.recoveryCheckpoints[0].CallID != "write-1" {
		t.Fatalf("checkpoint lineage lost: %+v", request.recoveryCheckpoints)
	}
	if request.recoveryID != "recovery-1" || request.recoverySessionID != "session-1" {
		t.Fatalf("durable recovery identity lost: %+v", request)
	}
	if request.recoveryMemoryQuery != "raw retry" {
		t.Fatalf("recovery memory query lost: %+v", request)
	}
}

func TestCancelProcessingDrainsQueue(t *testing.T) {
	a := &App{}
	a.enqueuePending("stale-1")
	a.enqueuePending("stale-2")
	a.CancelProcessing() // no in-flight request, but it must still drain the queue
	if got := a.pendingCount(); got != 0 {
		t.Errorf("CancelProcessing must drain the type-ahead queue, %d left", got)
	}
}

// TestPendingQueueFIFO pins the type-ahead contract: messages queued while
// processing come back in submission order (the old single-slot field REPLACED
// the previous message — user input was silently dropped).
func TestPendingQueueFIFO(t *testing.T) {
	a := &App{}
	for i := 1; i <= 3; i++ {
		pos, ok := a.enqueuePending(fmt.Sprintf("msg-%d", i))
		if !ok || pos != i {
			t.Fatalf("enqueue %d: pos=%d ok=%v, want pos=%d ok=true", i, pos, ok, i)
		}
	}
	if got := a.pendingCount(); got != 3 {
		t.Fatalf("pendingCount = %d, want 3", got)
	}

	for i := 1; i <= 3; i++ {
		msg, remaining, ok := a.dequeuePending()
		if !ok {
			t.Fatalf("dequeue %d: queue unexpectedly empty", i)
		}
		if want := fmt.Sprintf("msg-%d", i); msg != want {
			t.Fatalf("dequeue %d: got %q, want %q (FIFO order)", i, msg, want)
		}
		if remaining != 3-i {
			t.Fatalf("dequeue %d: remaining=%d, want %d", i, remaining, 3-i)
		}
	}
	if _, _, ok := a.dequeuePending(); ok {
		t.Fatal("dequeue on empty queue should report ok=false")
	}
}

// TestPendingQueueCapRejectsNewest pins the overflow policy: the queue is
// bounded and overflow REJECTS the new message (explicit feedback at the call
// site) — it never silently drops an older queued message.
func TestPendingQueueCapRejectsNewest(t *testing.T) {
	a := &App{}
	for i := 0; i < maxPendingQueue; i++ {
		if _, ok := a.enqueuePending(fmt.Sprintf("m%d", i)); !ok {
			t.Fatalf("enqueue %d should succeed under the cap", i)
		}
	}
	if pos, ok := a.enqueuePending("overflow"); ok {
		t.Fatalf("enqueue past cap should be rejected, got pos=%d ok=true", pos)
	}
	// The original head must be intact (nothing was displaced).
	msg, _, ok := a.dequeuePending()
	if !ok || msg != "m0" {
		t.Fatalf("head after overflow = %q ok=%v, want m0", msg, ok)
	}
}

// TestPendingSnapshotIsACopy — mutating the snapshot must not corrupt the
// queue (snapshots go into recovery files and get redacted in place).
func TestPendingSnapshotIsACopy(t *testing.T) {
	a := &App{}
	a.enqueuePending("secret-one")
	a.enqueuePending("two")

	snap := a.pendingSnapshot()
	if len(snap) != 2 || snap[0] != "secret-one" {
		t.Fatalf("snapshot = %v, want [secret-one two]", snap)
	}
	snap[0] = "[REDACTED]" // what SaveRecovery's redactor does in place

	msg, _, _ := a.dequeuePending()
	if msg != "secret-one" {
		t.Fatalf("queue corrupted by snapshot mutation: head = %q", msg)
	}

	var empty App
	if got := empty.pendingSnapshot(); got != nil {
		t.Fatalf("empty queue snapshot should be nil, got %v", got)
	}
}

// TestCancelProcessingResetsStopHookActive pins finding #12: a dropped Stop-hook
// continuation (its queued message drained on Esc, or its in-flight turn aborted)
// must not leave stopHookActive=true — that would make the NEXT user turn be
// treated as the continuation and silently skip its Stop hooks.
func TestCancelProcessingResetsStopHookActive(t *testing.T) {
	a := &App{}
	a.stopHookActive = true
	a.CancelProcessing()
	if a.stopHookActive {
		t.Error("CancelProcessing must reset stopHookActive so the next user turn's Stop hooks run")
	}
}
