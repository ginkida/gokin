package client

import (
	"context"
	"strings"
	"time"
)

// IsOverloadError reports whether err is a transient, self-resolving provider
// capacity signal — as opposed to a hard failure (auth, quota, bad request,
// context overflow). These deserve a SEPARATE, far more patient retry budget
// than ordinary retryable errors: the server is momentarily busy and WILL come
// back, so the right behavior is to wait it out rather than surface a failure
// and stop the agent.
//
// Detection is keyword-based, which reliably covers every overload source gokin
// sees because each one embeds a stable keyword in its error string:
//   - GLM/Z.AI 1301/1302/1303/1305 (concurrency/throughput/overload) → the
//     wrapped error embeds "overloaded" (see classifyGLMErrorCode),
//   - GLM 1210 → embeds "rate limit",
//   - Anthropic-style "overloaded_error" / HTTP 529 → "overloaded",
//   - Anthropic "rate_limit_error" / HTTP 429 → "rate_limit" / "too many requests".
//
// Keyword matching (rather than numeric-code matching) avoids false positives
// from timing/token strings like "529ms" or "1305 tokens".
func IsOverloadError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "overloaded") ||
		strings.Contains(s, "rate limit") ||
		strings.Contains(s, "rate_limit") ||
		strings.Contains(s, "too many requests")
}

// IsTransientProviderError reports whether err is an INFRASTRUCTURE-level
// failure that a long-lived background task (e.g. a /loop iteration) should
// wait out and retry later, rather than count as a genuine task failure that
// trips an auto-pause breaker. It is the union of provider overload/rate-limit
// (IsOverloadError) and clear network faults.
//
// Deliberately NARROWER than IsRetryableError: it EXCLUDES the ambiguous
// "timeout" / "deadline exceeded" / "EOF" signals, because those can equally
// mean the agent's OWN long task ran out of time (a real task problem) rather
// than the provider being unreachable. Misclassifying a stuck task as
// "transient" would keep a broken loop alive forever; keeping the set tight
// means only unambiguous infra faults get the patient loop-level backoff.
func IsTransientProviderError(err error) bool {
	if err == nil {
		return false
	}
	if IsOverloadError(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "no such host") ||
		strings.Contains(s, "service unavailable") ||
		strings.Contains(s, "bad gateway") ||
		strings.Contains(s, "temporarily")
}

// OverloadRetryPolicy controls how patiently a transient provider overload is
// waited out. Overloads get a separate, generous budget (see IsOverloadError):
// the agent keeps retrying with capped exponential backoff until the provider
// recovers, bounded by MaxRetries (a hard backstop) and MaxTotal (a wall-clock
// budget) so it can never wait truly forever. The wait is always ctx-cancellable
// (Esc aborts), and the early attempts are short (BaseDelay) so a brief spike
// recovers fast while a sustained one ramps up to MaxDelay.
type OverloadRetryPolicy struct {
	MaxRetries int           // hard cap on overload retries (backstop)
	BaseDelay  time.Duration // initial backoff
	MaxDelay   time.Duration // per-attempt backoff cap
	MaxTotal   time.Duration // total time budget across all overload waits this request/round
}

// DefaultOverloadRetryPolicy returns sensible patience for provider overloads.
// Backoff ramps 3s→6s→12s→24s→45s(cap); MaxTotal (10m) is the effective bound,
// allowing ~15 attempts before surfacing the error with a switch-provider hint.
func DefaultOverloadRetryPolicy() OverloadRetryPolicy {
	return OverloadRetryPolicy{
		MaxRetries: 40,
		BaseDelay:  3 * time.Second,
		MaxDelay:   45 * time.Second,
		MaxTotal:   10 * time.Minute,
	}
}

func normalizeOverloadRetryPolicy(p OverloadRetryPolicy) OverloadRetryPolicy {
	def := DefaultOverloadRetryPolicy()
	if p.MaxRetries <= 0 {
		p.MaxRetries = def.MaxRetries
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = def.BaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = def.MaxDelay
	}
	if p.MaxTotal <= 0 {
		p.MaxTotal = def.MaxTotal
	}
	return p
}

// DecideOverloadRetry decides whether to keep waiting out a provider overload.
//
//   - err must be an overload error (callers gate on IsOverloadError; this
//     re-checks defensively and returns no-retry for anything else, so a
//     misclassified error can never be parked on the patient budget).
//   - overloadCount is the number of overload waits already performed this
//     request/round; elapsed is the cumulative time already spent waiting.
//
// The decision honors ctx cancellation, the MaxRetries backstop, and the
// MaxTotal wall-clock budget. Delay is capped exponential backoff with jitter,
// trimmed so a final wait can't overshoot the remaining budget.
func DecideOverloadRetry(policy OverloadRetryPolicy, err error, overloadCount int, elapsed time.Duration, ctx context.Context) StreamRetryDecision {
	if err == nil || !IsOverloadError(err) {
		return StreamRetryDecision{}
	}
	if ctx != nil && ContextErr(ctx) != nil {
		return StreamRetryDecision{}
	}
	policy = normalizeOverloadRetryPolicy(policy)
	if overloadCount >= policy.MaxRetries || elapsed >= policy.MaxTotal {
		return StreamRetryDecision{}
	}

	delay := CalculateBackoff(policy.BaseDelay, overloadCount, policy.MaxDelay)
	if remaining := policy.MaxTotal - elapsed; remaining > 0 && delay > remaining {
		delay = remaining
	}
	return StreamRetryDecision{
		ShouldRetry: true,
		Delay:       delay,
		Reason:      "overloaded",
	}
}
