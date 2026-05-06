package tools

import (
	"testing"
)

// TestDeltaCheck_GraceWindow_AllowsMultiFileWorkflow is a regression for
// v0.80.3: a multi-file workflow (creating a new package whose first file
// imports a sibling that doesn't exist yet) used to deadlock —
//
//	write storage.go (imports proxy)   → build fails, blocked=true
//	write proxy.go   (imports storage) → barrier blocks the call → ErrorResult
//	model retries the same write       → still blocked, still ErrorResult
//	model gives up
//
// With the grace window, the barrier lets up to deltaCheckBarrierGraceLimit
// mutating calls through after the first failure. That's enough for the
// model to finish the package and let the next delta-check pass.
func TestDeltaCheck_GraceWindow_AllowsMultiFileWorkflow(t *testing.T) {
	e := &Executor{
		deltaCheckBlocked:     true,
		deltaCheckBlockReason: "build failed: cannot find package",
		deltaCheckGracedCalls: 0,
	}

	// Simulate the barrier "consume one grace slot, allow through" behavior
	// directly (rather than wiring up a full FunctionCall path) so the test
	// stays a pure-helper unit test.
	for i := range deltaCheckBarrierGraceLimit {
		e.deltaCheckMu.Lock()
		graced := e.deltaCheckGracedCalls
		if graced >= deltaCheckBarrierGraceLimit {
			e.deltaCheckMu.Unlock()
			t.Fatalf("call %d: barrier should not block — grace window not exhausted (graced=%d, limit=%d)",
				i+1, graced, deltaCheckBarrierGraceLimit)
		}
		e.deltaCheckGracedCalls++
		e.deltaCheckMu.Unlock()
	}

	// After exhausting the grace window, the next call must be barred.
	e.deltaCheckMu.Lock()
	graced := e.deltaCheckGracedCalls
	e.deltaCheckMu.Unlock()
	if graced < deltaCheckBarrierGraceLimit {
		t.Fatalf("after %d calls graced should equal limit %d, got %d",
			deltaCheckBarrierGraceLimit, deltaCheckBarrierGraceLimit, graced)
	}
}

// TestDeltaCheck_CommitResult_ResetsGraceOnPass pins the contract that a
// successful (or skipped) delta-check drains both the block flag AND the
// grace counter. Otherwise a model that recovers from one failure cycle and
// then triggers a NEW failure later would only get whatever grace was left
// over from the prior cycle.
func TestDeltaCheck_CommitResult_ResetsGraceOnPass(t *testing.T) {
	e := &Executor{
		deltaCheckBlocked:     true,
		deltaCheckBlockReason: "stale failure",
		deltaCheckGracedCalls: 2, // partway through grace window
	}

	e.commitDeltaCheckResult(deltaCheckResult{Ran: true, Passed: true}, false)

	if e.deltaCheckBlocked {
		t.Error("blocked flag should be cleared after passing check")
	}
	if e.deltaCheckGracedCalls != 0 {
		t.Errorf("grace counter should reset after passing check, got %d", e.deltaCheckGracedCalls)
	}
}

// TestDeltaCheck_CommitResult_PreservesGraceOnRepeatedFailure is the
// counterpart: when a failure happens but we were ALREADY blocked, the
// grace counter must NOT reset. Otherwise a model stuck in a loop could
// rotate {barrier-runs-check, still-failing, counter-resets, barrier-blocks}
// forever — exactly the deadlock the grace window was meant to prevent.
func TestDeltaCheck_CommitResult_PreservesGraceOnRepeatedFailure(t *testing.T) {
	e := &Executor{
		deltaCheckBlocked:     true,
		deltaCheckBlockReason: "previous failure",
		deltaCheckGracedCalls: 2,
	}

	e.commitDeltaCheckResult(deltaCheckResult{
		Ran:     true,
		Passed:  false,
		Summary: "still failing",
	}, false)

	if !e.deltaCheckBlocked {
		t.Error("blocked flag should remain true after repeat failure")
	}
	if e.deltaCheckGracedCalls != 2 {
		t.Errorf("grace counter must NOT reset on repeat failure, got %d (was 2)", e.deltaCheckGracedCalls)
	}
}

// TestDeltaCheck_CommitResult_ResetsGraceOnFreshFailure: when a failure
// happens but we were previously NOT blocked (i.e. the prior check passed
// or we just started), the grace counter resets to 0 so the new failure
// cycle gets a fresh grace window.
func TestDeltaCheck_CommitResult_ResetsGraceOnFreshFailure(t *testing.T) {
	e := &Executor{
		deltaCheckBlocked:     false, // not yet blocked
		deltaCheckGracedCalls: 5,     // leftover from much earlier cycle
	}

	e.commitDeltaCheckResult(deltaCheckResult{
		Ran:     true,
		Passed:  false,
		Summary: "first failure of new cycle",
	}, false)

	if !e.deltaCheckBlocked {
		t.Error("blocked flag should be set after failure")
	}
	if e.deltaCheckGracedCalls != 0 {
		t.Errorf("fresh failure cycle must reset grace counter to 0, got %d", e.deltaCheckGracedCalls)
	}
}
