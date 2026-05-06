package tools

import (
	"testing"
)

// TestExecutor_ResetSession_DrainsDeltaCheckState is a regression for v0.79.7:
// ResetSession used to leave several delta-check fields populated after /clear.
// `deltaPendingPaths` was the actually-leaky one — paths recorded as
// "modified since last successful delta check" survived /clear, so the first
// write in a new conversation could trigger a check based on prior-conversation
// state. The other fields were leaky-but-harmless (stale block reason, last
// hash) — drained for cleanliness so they don't surface in future code paths.
func TestExecutor_ResetSession_DrainsDeltaCheckState(t *testing.T) {
	e := &Executor{
		deltaCheckBlocked:     true,
		deltaCheckBlockReason: "stale: previous conversation had a failed go vet",
		deltaCheckLastHash:    "stale-hash-from-previous-convo",
		deltaBaselineCaptured: true,
		deltaBaselinePaths: map[string]struct{}{
			"old/file.go":     {},
			"old/another.go":  {},
		},
		deltaPendingPaths: map[string]struct{}{
			"prev/edit1.go": {},
			"prev/edit2.go": {},
		},
		deltaCheckLastResult: &deltaCheckResult{Ran: true, Passed: false},
	}

	e.ResetSession()

	if e.deltaCheckBlocked {
		t.Error("deltaCheckBlocked should be false after reset")
	}
	if e.deltaCheckBlockReason != "" {
		t.Errorf("deltaCheckBlockReason should be empty, got %q", e.deltaCheckBlockReason)
	}
	if e.deltaCheckLastHash != "" {
		t.Errorf("deltaCheckLastHash should be empty, got %q", e.deltaCheckLastHash)
	}
	if e.deltaBaselineCaptured {
		t.Error("deltaBaselineCaptured should be false after reset")
	}
	if e.deltaCheckLastResult != nil {
		t.Error("deltaCheckLastResult should be nil after reset")
	}
	if len(e.deltaPendingPaths) != 0 {
		t.Errorf("deltaPendingPaths should be empty, got %d entries", len(e.deltaPendingPaths))
	}
	if len(e.deltaBaselinePaths) != 0 {
		t.Errorf("deltaBaselinePaths should be empty, got %d entries", len(e.deltaBaselinePaths))
	}
}

// TestExecutor_ResetSession_NilTrackersSafe verifies ResetSession doesn't
// panic when readTracker / writeTracker are nil (legitimate for tests that
// build a minimal Executor for unit-testing helpers).
func TestExecutor_ResetSession_NilTrackersSafe(t *testing.T) {
	e := &Executor{}
	// Should not panic.
	e.ResetSession()
}
