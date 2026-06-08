package loops

import (
	"testing"
	"time"
)

// Completion takes precedence over the consecutive-failure breaker. A loop that
// reaches MaxIterations on a FAILING iteration while ConsecutiveFailures has
// crossed the limit must end Completed — NOT Paused+AutoPaused — because a
// terminal "completed" loop should never become resumable just because its last
// iterations failed. If the two checks in AppendIteration were ever reordered
// (breaker before the completion early-return), this loop would wrongly be
// auto-paused. This is the invariant-#7 precedence guard, which the existing
// breaker test (no MaxIterations) and completion test (all-success) each miss.
func TestLoop_AppendIteration_CompletionBeatsBreaker(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	// MaxIterations == the failure limit, so on the very last (failing)
	// iteration the loop BOTH completes AND has ConsecutiveFailures >= limit.
	l := &Loop{
		ID: "l", Task: "t", Mode: ModeSelfPaced, Status: StatusRunning,
		MaxIterations: ConsecutiveFailureLimit, CreatedAt: start,
	}
	for i := range ConsecutiveFailureLimit {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: start, Duration: time.Second, OK: false})
	}

	if l.ConsecutiveFailures < ConsecutiveFailureLimit {
		t.Fatalf("precondition: ConsecutiveFailures = %d, want >= %d", l.ConsecutiveFailures, ConsecutiveFailureLimit)
	}
	if l.Status != StatusCompleted {
		t.Errorf("status = %s, want completed (completion must beat the breaker)", l.Status)
	}
	if l.AutoPaused {
		t.Error("AutoPaused = true; a completed loop must not be auto-paused")
	}
	// And it must be terminal — never fired again.
	if l.IsDue(start.Add(time.Hour)) {
		t.Error("a completed loop must never be due")
	}
}
