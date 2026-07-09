package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"gokin/internal/client"
)

// TestIsAutoResumableError pins the error classification that decides whether
// the auto-resume (compact + retry) path fires. The #1 target is
// ErrModelRoundTimeout (the "agent stopped at 14m with GLM" failure); the
// exclusions are errors where retrying with a compacted context would
// deterministically fail again (auth, terminal, user-cancel, circuit-open).
func TestIsAutoResumableError(t *testing.T) {
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"model round timeout", timeoutErr, true},
		{"nil error", nil, false},
		{"context cancelled", context.Canceled, false},
		{"request circuit open", ErrRequestCircuitOpen, false},
		{"empty model response", client.ErrEmptyModelResponse, true},
		{"generic retryable (5xx)", &client.HTTPError{StatusCode: 500, Message: "internal server error"}, true},
		{"non-retryable 400", &client.HTTPError{StatusCode: 400, Message: "bad request"}, false},
		// Overloads have their OWN patient budget (~10min, v0.100.46) whose
		// documented contract is "when it exhausts, the error surfaces
		// actionable". Auto-resume must not bolt extra silent cycles onto it —
		// and compaction can't fix a provider capacity problem anyway.
		{"provider overload after patient budget", errors.New("GLM server overloaded, please retry later"), false},
		{"rate limit after patient budget", errors.New("429 rate_limit_error: too many requests"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isAutoResumableError(tc.err)
			if got != tc.want {
				t.Errorf("isAutoResumableError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsAutoResumableError_TerminalProviderError verifies that terminal provider
// errors (GLM 5-hour cap 1308, invalid key) are NOT auto-resumed — retrying
// with a compacted context would just hit the same terminal error.
func TestIsAutoResumableError_TerminalProviderError(t *testing.T) {
	// TerminalProviderError is the typed sentinel for GLM 5-hour cap (1308),
	// insufficient balance, auth failure — retrying with a compacted context
	// would hit the same terminal error.
	terminalErr := &client.TerminalProviderError{
		Code:    "1308",
		Status:  429,
		Message: "5-hour usage cap exceeded",
	}
	if !client.IsTerminalProviderError(terminalErr) {
		t.Fatalf("setup error: expected terminal provider error, got %v", terminalErr)
	}
	if isAutoResumableError(terminalErr) {
		t.Error("terminal provider error should NOT be auto-resumable")
	}
}

// TestScheduleAutoResume_BudgetAndDelays pins the budget + delay progression:
// two attempts (15s, 30s), then exhausted. Keyed by message so different
// messages get independent budgets.
func TestScheduleAutoResume_BudgetAndDelays(t *testing.T) {
	a := &App{
		autoResumeCount: make(map[string]int),
	}
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)

	msg := "do the task"

	// Attempt 1 → 15s
	attempt, delay, ok := a.scheduleAutoResume(msg, timeoutErr)
	if !ok {
		t.Fatal("first schedule should succeed")
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1", attempt)
	}
	if delay != 15*time.Second {
		t.Errorf("delay = %v, want 15s", delay)
	}

	// Attempt 2 → 30s
	attempt, delay, ok = a.scheduleAutoResume(msg, timeoutErr)
	if !ok {
		t.Fatal("second schedule should succeed")
	}
	if attempt != 2 {
		t.Errorf("attempt = %d, want 2", attempt)
	}
	if delay != 30*time.Second {
		t.Errorf("delay = %v, want 30s", delay)
	}

	// Attempt 3 → exhausted
	_, _, ok = a.scheduleAutoResume(msg, timeoutErr)
	if ok {
		t.Error("third schedule should be exhausted (budget = 2)")
	}
}

// TestScheduleAutoResume_DifferentMessagesIndependentBudgets verifies the
// per-message keying: exhausting the budget on one message does NOT block
// a different message from getting its own auto-resume.
func TestScheduleAutoResume_DifferentMessagesIndependentBudgets(t *testing.T) {
	a := &App{
		autoResumeCount: make(map[string]int),
	}
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)

	// Exhaust budget on message A
	for i := 0; i < maxAutoResumeAttempts; i++ {
		_, _, ok := a.scheduleAutoResume("message A", timeoutErr)
		if !ok {
			t.Fatalf("attempt %d for message A should succeed", i+1)
		}
	}

	// Message B gets its own fresh budget
	attempt, _, ok := a.scheduleAutoResume("message B", timeoutErr)
	if !ok {
		t.Fatal("message B should get its own budget")
	}
	if attempt != 1 {
		t.Errorf("message B attempt = %d, want 1 (independent budget)", attempt)
	}
}

// TestScheduleAutoResume_NonResumableErrorReturnsFalse verifies that a
// non-resumable error (e.g. context.Canceled) is rejected immediately without
// consuming any budget.
func TestScheduleAutoResume_NonResumableErrorReturnsFalse(t *testing.T) {
	a := &App{
		autoResumeCount: make(map[string]int),
	}

	_, _, ok := a.scheduleAutoResume("msg", context.Canceled)
	if ok {
		t.Error("non-resumable error should not be scheduled")
	}

	// Budget should NOT have been consumed
	if len(a.autoResumeCount) != 0 {
		t.Errorf("non-resumable error should not consume budget, got count map: %v", a.autoResumeCount)
	}
}

// TestClearAutoResume verifies the counter is cleared on success so the next
// failure for the same message gets a fresh budget.
func TestClearAutoResume(t *testing.T) {
	a := &App{
		autoResumeCount: make(map[string]int),
	}
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)
	msg := "the task"

	// Consume one attempt
	_, _, _ = a.scheduleAutoResume(msg, timeoutErr)
	if len(a.autoResumeCount) != 1 {
		t.Fatalf("expected 1 entry after schedule, got %d", len(a.autoResumeCount))
	}

	// Clear it (simulates success after auto-resume)
	a.clearAutoResume(msg)
	if len(a.autoResumeCount) != 0 {
		t.Errorf("expected 0 entries after clear, got %d", len(a.autoResumeCount))
	}

	// Next failure gets a fresh budget (attempt 1 again)
	attempt, _, ok := a.scheduleAutoResume(msg, timeoutErr)
	if !ok {
		t.Fatal("should get fresh budget after clear")
	}
	if attempt != 1 {
		t.Errorf("attempt = %d, want 1 (fresh budget after clear)", attempt)
	}
}

// TestAutoResumeReason verifies the human-readable label for the UI toast.
func TestAutoResumeReason(t *testing.T) {
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)
	if got := autoResumeReason(timeoutErr); got != "model round timeout" {
		t.Errorf("reason for timeout = %q, want 'model round timeout'", got)
	}

	// Unknown error → DetectFailureTelemetry returns "other", not empty
	if got := autoResumeReason(errors.New("something weird")); got != "other" {
		t.Errorf("reason for unknown = %q, want 'other'", got)
	}
}

// TestIsContextSizeError pins the classification used to skip a retry whose
// compaction removed nothing. Model round timeout IS context-size-driven (a
// large context forces longer reasoning → 14m cap); other resumable errors are
// not, so they still retry even with an unchanged context.
func TestIsContextSizeError(t *testing.T) {
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)
	if !isContextSizeError(timeoutErr) {
		t.Error("model round timeout should be a context-size error")
	}

	// Empty model response is resumable but NOT classified as context-size — it's
	// cheap to retry and may be transient, so the no-compaction skip doesn't apply.
	if isContextSizeError(client.ErrEmptyModelResponse) {
		t.Error("empty model response should NOT be a context-size error")
	}

	// A transient network error is not context-size-driven.
	if isContextSizeError(errors.New("connection reset")) {
		t.Error("generic network error should NOT be a context-size error")
	}

	if isContextSizeError(nil) {
		t.Error("nil error should not be a context-size error")
	}
}

// TestRefundAutoResume verifies that refundAutoResume reverses exactly one
// increment, deleting the key when it reaches zero. This is the safety net for
// the skip path: an attempt that was scheduled but never run must not consume
// the budget.
func TestRefundAutoResume(t *testing.T) {
	a := &App{
		autoResumeCount: make(map[string]int),
	}
	timeoutErr := client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout)
	msg := "the task"

	// No-op when nothing was scheduled (defensive — shouldn't panic or go negative).
	a.refundAutoResume(msg)
	if len(a.autoResumeCount) != 0 {
		t.Errorf("refund with no prior schedule should be a no-op, got %v", a.autoResumeCount)
	}

	// Schedule two attempts, then refund one → back to count 1.
	_, _, _ = a.scheduleAutoResume(msg, timeoutErr)
	_, _, _ = a.scheduleAutoResume(msg, timeoutErr)
	a.refundAutoResume(msg)
	if a.autoResumeCount[rateLimitRetryKey(msg)] != 1 {
		t.Errorf("after refund of 2, count should be 1, got %v", a.autoResumeCount)
	}

	// Refund back to zero → key deleted (clean map, no phantom zero entries).
	a.refundAutoResume(msg)
	if _, ok := a.autoResumeCount[rateLimitRetryKey(msg)]; ok {
		t.Errorf("after refund to zero, key should be deleted, got %v", a.autoResumeCount)
	}

	// Budget is truly restored: can schedule 2 full attempts again.
	attempt, _, ok := a.scheduleAutoResume(msg, timeoutErr)
	if !ok || attempt != 1 {
		t.Fatalf("after full refund, should get fresh attempt 1, got ok=%v attempt=%d", ok, attempt)
	}
	attempt2, _, ok2 := a.scheduleAutoResume(msg, timeoutErr)
	if !ok2 || attempt2 != 2 {
		t.Fatalf("second attempt after refund should succeed, got ok=%v attempt=%d", ok2, attempt2)
	}
}
