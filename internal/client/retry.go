package client

import (
	"context"
	"errors"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RetryConfig holds retry configuration used across all client implementations.
type RetryConfig struct {
	MaxRetries int           // Maximum number of retry attempts
	RetryDelay time.Duration // Initial delay between retries
	MaxDelay   time.Duration // Maximum backoff delay (cap)
}

// DefaultRetryConfig returns sensible retry defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 10,
		RetryDelay: 1 * time.Second,
		MaxDelay:   30 * time.Second,
	}
}

// StreamRetryPolicy defines provider-agnostic retry behavior for stream failures.
type StreamRetryPolicy struct {
	// MaxRetries applies to cold stream idle timeouts and generic retryable errors.
	MaxRetries int
	// MaxPartialRetries applies when stream idle happened after partial output.
	MaxPartialRetries int
	// BaseDelay is the initial backoff delay before retry.
	BaseDelay time.Duration
	// MaxDelay caps exponential backoff growth.
	MaxDelay time.Duration
}

// StreamRetryOptions controls policy behavior for a specific call site.
type StreamRetryOptions struct {
	// AllowPartial enables retries for partial stream-idle errors.
	AllowPartial bool
}

// StreamRetryDecision is the result of retry policy evaluation.
type StreamRetryDecision struct {
	ShouldRetry bool
	Delay       time.Duration
	Reason      string
	Partial     bool
}

// DefaultStreamRetryPolicy returns defaults shared across providers and runtimes.
func DefaultStreamRetryPolicy() StreamRetryPolicy {
	return StreamRetryPolicy{
		MaxRetries:        2,
		MaxPartialRetries: 1,
		BaseDelay:         2 * time.Second,
		MaxDelay:          30 * time.Second,
	}
}

func normalizeStreamRetryPolicy(policy StreamRetryPolicy) StreamRetryPolicy {
	def := DefaultStreamRetryPolicy()

	// Backward-compatible defaulting for zero-value policy.
	// This keeps existing callers that pass StreamRetryPolicy{} working.
	if policy == (StreamRetryPolicy{}) {
		return def
	}

	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	if policy.MaxPartialRetries < 0 {
		policy.MaxPartialRetries = 0
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = def.BaseDelay
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = def.MaxDelay
	}
	return policy
}

// DecideStreamRetry evaluates whether a failed stream request should be retried.
// retryCount and partialRetryCount are already-used retries for their categories.
func DecideStreamRetry(
	policy StreamRetryPolicy,
	err error,
	retryCount int,
	partialRetryCount int,
	ctx context.Context,
	options StreamRetryOptions,
) StreamRetryDecision {
	if err == nil {
		return StreamRetryDecision{}
	}
	if ctx != nil && ContextErr(ctx) != nil {
		return StreamRetryDecision{}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrModelRoundTimeout) {
		return StreamRetryDecision{}
	}

	policy = normalizeStreamRetryPolicy(policy)

	var sitErr *ErrStreamIdleTimeout
	if errors.As(err, &sitErr) {
		// Partial stream-idle retries are expensive, make them explicit and capped.
		if sitErr.Partial {
			if !options.AllowPartial || partialRetryCount >= policy.MaxPartialRetries {
				return StreamRetryDecision{}
			}
			return StreamRetryDecision{
				ShouldRetry: true,
				Delay:       CalculateBackoff(policy.BaseDelay, partialRetryCount, policy.MaxDelay),
				Reason:      string(FailureReasonStreamIdleTimeout),
				Partial:     true,
			}
		}

		if retryCount >= policy.MaxRetries {
			return StreamRetryDecision{}
		}
		return StreamRetryDecision{
			ShouldRetry: true,
			Delay:       CalculateBackoff(policy.BaseDelay, retryCount, policy.MaxDelay),
			Reason:      string(FailureReasonStreamIdleTimeout),
		}
	}

	if !IsRetryableError(err) || retryCount >= policy.MaxRetries {
		return StreamRetryDecision{}
	}

	reason := DetectFailureTelemetry(err).Reason
	if reason == "" {
		reason = string(FailureReasonOther)
	}
	return StreamRetryDecision{
		ShouldRetry: true,
		Delay:       CalculateBackoff(policy.BaseDelay, retryCount, policy.MaxDelay),
		Reason:      reason,
	}
}

// AdaptiveRetryConfig returns a retry config adjusted by the provider's health score.
// Healthy providers (score > 0) get shorter delays and more retries.
// Unhealthy providers (score < 0) get longer delays and fewer retries.
func AdaptiveRetryConfig(provider string) RetryConfig {
	base := DefaultRetryConfig()
	h := getProviderHealth(provider)

	switch {
	case h.Score >= 5:
		// Very healthy — trust this provider, retry aggressively
		base.RetryDelay = 500 * time.Millisecond
		base.MaxDelay = 15 * time.Second
	case h.Score >= 0:
		// Healthy or new — normal retries
		// keep defaults
	case h.Score >= -5:
		// Slightly unhealthy — back off a bit
		base.RetryDelay = 2 * time.Second
		base.MaxDelay = 45 * time.Second
		base.MaxRetries = 7
	case h.Score >= -10:
		// Unhealthy — longer backoff, fewer retries
		base.RetryDelay = 4 * time.Second
		base.MaxDelay = 60 * time.Second
		base.MaxRetries = 5
	default:
		// Very unhealthy (score < -10) — minimal retries, long delays
		base.RetryDelay = 8 * time.Second
		base.MaxDelay = 90 * time.Second
		base.MaxRetries = 3
	}

	if h.FailureStreak >= 5 {
		base.RetryDelay = max(base.RetryDelay, 5*time.Second)
		base.MaxRetries = min(base.MaxRetries, 4)
	}

	return base
}

// AdaptiveStreamRetryPolicy returns a stream retry policy adjusted by provider health.
func AdaptiveStreamRetryPolicy(provider string) StreamRetryPolicy {
	base := DefaultStreamRetryPolicy()
	h := getProviderHealth(provider)

	switch {
	case h.Score >= 5:
		base.MaxRetries = 3
		base.BaseDelay = 1 * time.Second
	case h.Score >= 0:
		// keep defaults
	case h.Score >= -5:
		base.BaseDelay = 3 * time.Second
		base.MaxDelay = 45 * time.Second
	default:
		base.MaxRetries = 1
		base.BaseDelay = 5 * time.Second
		base.MaxDelay = 60 * time.Second
	}

	return base
}

// CalculateBackoff calculates exponential backoff with jitter.
// This prevents thundering herd problem when many clients retry simultaneously.
func CalculateBackoff(baseDelay time.Duration, attempt int, maxDelay time.Duration) time.Duration {
	// Exponential backoff: baseDelay * 2^attempt
	delay := baseDelay * time.Duration(1<<uint(attempt))
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter: random value between 0 and 25% of delay
	jitter := time.Duration(rand.Int63n(int64(delay / 4)))
	return delay + jitter
}

// ParseRetryAfter extracts Retry-After duration from an HTTP response.
// Supports both seconds (integer) and HTTP-date formats.
// Returns 0 if the header is absent or unparseable.
func ParseRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	header := resp.Header.Get("Retry-After")
	if header == "" {
		return 0
	}

	// Try parsing as integer seconds first (most common for APIs)
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}

	// Try parsing as HTTP-date
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}

	return 0
}
