package app

import (
	"context"
	"errors"
	"time"

	"gokin/internal/client"
	"gokin/internal/logging"
)

const (
	// maxAutoResumeAttempts is the maximum number of auto-resume attempts
	// (compact + retry) before we give up and surface the error to the user.
	// Each attempt compacts the context (reducing reasoning load) and retries
	// after a delay. Two attempts cover the common case: the first compaction
	// may not be enough if the context was already large, the second gets a
	// smaller, more focused context.
	maxAutoResumeAttempts = 2
)

// autoResumeDelays are the delays between auto-resume attempts. They give the
// provider time to recover (for overload/timeout) and the compaction time to
// settle. Indexed by attempt number (0-based).
var autoResumeDelays = []time.Duration{
	15 * time.Second,
	30 * time.Second,
}

// isAutoResumableError returns true for errors where an auto-resume (compact +
// retry) is likely to help — as opposed to errors that will deterministically
// fail again (auth, terminal provider errors, user cancellation).
//
// The key insight: model round timeout and retry-exhausted errors are often
// caused by a large context forcing long reasoning. Compacting the context
// reduces the reasoning load, making the next attempt more likely to succeed
// within the timeout. This is especially true for GLM with extended thinking.
func isAutoResumableError(err error) bool {
	if err == nil {
		return false
	}

	// User cancellation — never auto-resume (user explicitly stopped).
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Terminal provider errors (5-hour cap, invalid key, etc.) — will fail
	// again, no point retrying.
	if client.IsTerminalProviderError(err) {
		return false
	}

	// Request circuit open — the breaker already determined this provider is
	// down. Auto-resume would just hammer a dead provider.
	if errors.Is(err, ErrRequestCircuitOpen) {
		return false
	}

	// Provider overload — deliberately NOT auto-resumable. Overloads already
	// get their own far more patient budget (client.OverloadRetryPolicy,
	// ~10min at the request layer, v0.100.46); when THAT exhausts, the
	// documented contract is "the error surfaces actionable, not silent".
	// Letting the generic IsRetryableError branch below catch it would bolt
	// up to two more full patient cycles (potentially ~20 extra minutes) onto
	// an error the user was promised to see — and compaction doesn't help a
	// capacity problem anyway.
	if client.IsOverloadError(err) {
		return false
	}

	// Model round timeout — the #1 target. GLM with long reasoning >14m hits
	// this. Compacting reduces context → less reasoning → fits in timeout.
	if errors.Is(err, client.ErrModelRoundTimeout) {
		return true
	}

	// HTTP timeout — provider is slow/overloaded. Compact + retry gives it
	// a fresh chance with a smaller payload.
	ft := client.DetectFailureTelemetry(err)
	if ft.Reason == string(client.FailureReasonHTTPTimeout) {
		return true
	}

	// Stream idle timeout (cold) — provider stalled. Compact + retry.
	if ft.Reason == string(client.FailureReasonStreamIdleTimeout) {
		return true
	}

	// Empty model response after tools — sometimes caused by context being
	// too large for the model to generate a coherent response.
	if errors.Is(err, client.ErrEmptyModelResponse) {
		return true
	}

	// Generic retryable errors that exhausted the in-loop retry budget.
	// These include network errors, 5xx, etc. — compaction won't fix the root
	// cause but a smaller payload may succeed where a large one failed.
	if client.IsRetryableError(err) {
		return true
	}

	return false
}

// autoResumeReason returns a human-readable label for why the auto-resume was
// triggered, shown in the UI toast.
func autoResumeReason(err error) string {
	if errors.Is(err, client.ErrModelRoundTimeout) {
		return "model round timeout"
	}
	ft := client.DetectFailureTelemetry(err)
	if ft.Reason != "" {
		return string(ft.Reason)
	}
	return "error"
}

// scheduleAutoResume attempts to schedule an auto-resume (compact + retry) for
// a terminal error. It returns (attempt, delay, true) if a resume was scheduled,
// or (0, 0, false) if the budget is exhausted or the error is not resumable.
//
// The resume counter is keyed by the message content (same as rate-limit retry)
// so different messages get their own budgets.
func (a *App) scheduleAutoResume(message string, err error) (attempt int, delay time.Duration, scheduled bool) {
	if !isAutoResumableError(err) {
		return 0, 0, false
	}

	key := rateLimitRetryKey(message)

	a.autoResumeMu.Lock()
	defer a.autoResumeMu.Unlock()

	current := a.autoResumeCount[key]
	if current >= maxAutoResumeAttempts {
		return current, 0, false
	}

	next := current + 1
	a.autoResumeCount[key] = next

	if next-1 < len(autoResumeDelays) {
		delay = autoResumeDelays[next-1]
	} else {
		delay = autoResumeDelays[len(autoResumeDelays)-1]
	}
	return next, delay, true
}

// performAutoResumeCompaction runs an emergency context compaction before the
// auto-resume retry. It returns the number of messages removed. The compaction
// is the key recovery mechanism: a smaller context → less reasoning → the model
// finishes within the round timeout on the next attempt.
func (a *App) performAutoResumeCompaction() int {
	if a.contextManager == nil {
		return 0
	}
	removed := a.contextManager.EmergencyTruncate()
	if removed > 0 {
		logging.Info("auto-resume: compacted context",
			"messages_removed", removed)
	}
	return removed
}

// clearAutoResume clears the resume counter for a message (called on success).
func (a *App) clearAutoResume(message string) {
	key := rateLimitRetryKey(message)
	a.autoResumeMu.Lock()
	delete(a.autoResumeCount, key)
	a.autoResumeMu.Unlock()
}
