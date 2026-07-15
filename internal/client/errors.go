package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// APIError represents an API error with HTTP status code.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Message)
}

// ErrStreamIdleTimeout indicates the SSE stream stalled (no data for configured timeout).
type ErrStreamIdleTimeout struct {
	Timeout time.Duration
	Partial bool // true if some content was received before timeout
}

func (e *ErrStreamIdleTimeout) Error() string {
	if e.Partial {
		return fmt.Sprintf("stream idle timeout after partial response: no data for %v", e.Timeout)
	}
	return fmt.Sprintf("stream idle timeout: no data received for %v", e.Timeout)
}

// IsStreamIdleTimeout checks whether err is an ErrStreamIdleTimeout.
func IsStreamIdleTimeout(err error) bool {
	var sitErr *ErrStreamIdleTimeout
	return errors.As(err, &sitErr)
}

// FailureReason is a stable machine-readable category for request failures.
type FailureReason string

const (
	FailureReasonOther             FailureReason = "other"
	FailureReasonStreamIdleTimeout FailureReason = "stream_idle_timeout"
	FailureReasonModelRoundTimeout FailureReason = "model_round_timeout"
	FailureReasonContextCancel     FailureReason = "context_cancel"
	FailureReasonHTTPTimeout       FailureReason = "http_timeout"
)

// ErrModelRoundTimeout is the sentinel for executor-enforced round timeout.
var ErrModelRoundTimeout = errors.New("model round timeout")

// DefaultModelRoundTimeout is the HARD cap on a single model round (one
// SendMessage + stream collection) in BOTH agentic loops (executor + agent).
// It is a zombie backstop, NOT the primary stuck-guard — the activity-aware
// stream-idle timeout (anthropic.go, reset on each chunk) catches a stalled
// stream far sooner. So this is deliberately GENEROUS: a heavy-reasoning model
// (glm/deepseek) can legitimately stream thinking tokens for many minutes in one
// round, and a tight cap (the old 5m) killed those healthy rounds mid-thought —
// the model-round-timeout fires as a typed, NON-retryable error, so the agent/
// turn just failed (the "agent stopped at ~7m with 13m thinking" incident).
// 14m comfortably exceeds a realistic single-round thinking budget; the round
// helper still clamps to the parent's remaining deadline when the parent (e.g.
// a sub-agent overall timeout) is stricter. Single source of truth shared by
// tools.defaultModelRoundTimeout and agent.agentModelRoundTimeout (don't drift).
const DefaultModelRoundTimeout = 14 * time.Minute

// ErrEmptyModelResponse is the sentinel for a successful provider response
// that carried neither text nor tool calls.
var ErrEmptyModelResponse = errors.New("empty model response")

// EmptyModelResponseError carries context for empty successful model responses.
type EmptyModelResponseError struct {
	AfterToolResults bool
}

func (e *EmptyModelResponseError) Error() string {
	if e.AfterToolResults {
		return "model returned an empty response after tool results; work may be incomplete"
	}
	return "model returned an empty response"
}

func (e *EmptyModelResponseError) Unwrap() error {
	return ErrEmptyModelResponse
}

// TimeoutError carries typed timeout telemetry details.
type TimeoutError struct {
	Reason   FailureReason
	Provider string
	Timeout  time.Duration
	Err      error
}

func (e *TimeoutError) Error() string {
	base := string(e.Reason)
	if e.Provider != "" && e.Timeout > 0 {
		return fmt.Sprintf("%s (%s, %s): %v", base, e.Provider, e.Timeout, e.Err)
	}
	if e.Provider != "" {
		return fmt.Sprintf("%s (%s): %v", base, e.Provider, e.Err)
	}
	if e.Timeout > 0 {
		return fmt.Sprintf("%s (%s): %v", base, e.Timeout, e.Err)
	}
	return fmt.Sprintf("%s: %v", base, e.Err)
}

func (e *TimeoutError) Unwrap() error {
	return e.Err
}

// FailureTelemetry contains structured diagnostics for timeout/retry failures.
type FailureTelemetry struct {
	Reason   string
	Partial  bool
	Timeout  time.Duration
	Provider string
}

// ContextErr returns context cause when available (preserves timeout reason),
// falling back to ctx.Err().
func ContextErr(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

// NewModelRoundTimeoutError creates a typed model round timeout error.
func NewModelRoundTimeoutError(timeout time.Duration) error {
	return &TimeoutError{
		Reason:  FailureReasonModelRoundTimeout,
		Timeout: timeout,
		Err:     ErrModelRoundTimeout,
	}
}

// WrapProviderHTTPTimeout wraps timeout-like transport errors with typed telemetry.
func WrapProviderHTTPTimeout(err error, provider string, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if !isLikelyHTTPTimeout(err) {
		return err
	}
	return &TimeoutError{
		Reason:   FailureReasonHTTPTimeout,
		Provider: provider,
		Timeout:  timeout,
		Err:      err,
	}
}

// IsHTTPTimeout checks whether the error likely represents transport/header timeout.
func IsHTTPTimeout(err error) bool {
	if err == nil {
		return false
	}
	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		return timeoutErr.Reason == FailureReasonHTTPTimeout
	}
	return isLikelyHTTPTimeout(err)
}

// DetectFailureTelemetry classifies common failure reasons for logging/journaling.
func DetectFailureTelemetry(err error) FailureTelemetry {
	t := FailureTelemetry{Reason: string(FailureReasonOther)}
	if err == nil {
		return t
	}

	var timeoutErr *TimeoutError
	if errors.As(err, &timeoutErr) {
		if timeoutErr.Reason != "" {
			t.Reason = string(timeoutErr.Reason)
		}
		t.Provider = timeoutErr.Provider
		if timeoutErr.Timeout > 0 {
			t.Timeout = timeoutErr.Timeout
		}
	}

	var sitErr *ErrStreamIdleTimeout
	if errors.As(err, &sitErr) {
		t.Reason = string(FailureReasonStreamIdleTimeout)
		t.Partial = sitErr.Partial
		t.Timeout = sitErr.Timeout
		return t
	}

	if errors.Is(err, ErrModelRoundTimeout) {
		t.Reason = string(FailureReasonModelRoundTimeout)
		return t
	}
	if errors.Is(err, context.Canceled) {
		t.Reason = string(FailureReasonContextCancel)
		return t
	}
	if IsHTTPTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		t.Reason = string(FailureReasonHTTPTimeout)
		return t
	}

	return t
}

func isLikelyHTTPTimeout(err error) bool {
	if err == nil {
		return false
	}

	// Exclude explicit non-HTTP timeout categories.
	if errors.Is(err, ErrModelRoundTimeout) || IsStreamIdleTimeout(err) || errors.Is(err, context.Canceled) {
		return false
	}

	// Typed timeout checks.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// String fallback for wrapped third-party errors.
	msg := strings.ToLower(err.Error())
	patterns := []string{
		"client.timeout exceeded",
		"timeout awaiting response headers",
		"response header timeout",
		"awaiting headers",
		"i/o timeout",
		"tls handshake timeout",
		"http timeout",
	}
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func isRetryableHTTPStatusCode(statusCode int) bool {
	switch statusCode {
	case 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// IsRetryableAPIError returns true if the API error has a retryable status code.
func IsRetryableAPIError(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) && isRetryableHTTPStatusCode(apiErr.StatusCode) {
		return true
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) && isRetryableHTTPStatusCode(httpErr.StatusCode) {
		return true
	}

	// Some providers (MiniMax) return transient 400 "model_not_found" that resolves on retry.
	if errors.As(err, &httpErr) && httpErr.StatusCode == 400 {
		msg := strings.ToLower(httpErr.Message)
		if strings.Contains(msg, "model_not_found") || strings.Contains(msg, "model not found") {
			return true
		}
	}
	return false
}

// IsRateLimitError returns true when the error indicates API rate limiting (429).
func IsRateLimitError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 429 {
		return true
	}

	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 429 {
		return true
	}

	msg := err.Error()
	return containsLower(msg, "rate limit") || containsLower(msg, "too many requests")
}

// IsRetryableError checks if an error is retryable using proper type checks.
// Uses errors.Is/errors.As for typed errors, with string fallback only for untyped errors.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Typed checks first
	if errors.Is(err, context.Canceled) {
		return false
	}
	// A hard terminal provider failure (request/auth/account/plan/content) must never be
	// retried — retrying can't fix it and only delays the actionable error.
	// This is what makes GLM's 5-hour cap (1308) surface at once instead of
	// looping the small generic budget after IsOverloadError stops matching it.
	if IsTerminalProviderError(err) {
		return false
	}
	if errors.Is(err, ErrModelRoundTimeout) {
		return false
	}
	if errors.Is(err, ErrEmptyModelResponse) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if IsStreamIdleTimeout(err) {
		return true
	}
	if IsHTTPTimeout(err) {
		return true
	}

	// Network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// API errors with retryable status codes
	if IsRetryableAPIError(err) {
		return true
	}

	// String fallback only for untyped errors from third-party libraries
	msg := err.Error()
	untyped := []string{
		"rate limit",
		"overloaded",
		"temporarily",
		"eof",
		"tls handshake",
		"no such host",
	}
	for _, pattern := range untyped {
		if containsLower(msg, pattern) {
			return true
		}
	}

	return false
}

// containsLower checks if s contains substr (case-insensitive).
func containsLower(s, substr string) bool {
	return len(s) >= len(substr) && containsFold(s, substr)
}

func containsFold(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalFold(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// IsContextTooLongError returns true if the error indicates the request exceeded
// the model's context window (HTTP 400 with context/token-related message).
func IsContextTooLongError(err error) bool {
	if err == nil {
		return false
	}
	// Check typed HTTPError (Anthropic/GLM/DeepSeek)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == 400 {
		return messageIndicatesContextOverflow(strings.ToLower(httpErr.Message))
	}
	// Check typed APIError (Gemini)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 400 {
		return messageIndicatesContextOverflow(strings.ToLower(apiErr.Message))
	}
	// String fallback for untyped errors
	msg := strings.ToLower(err.Error())
	return (strings.Contains(msg, "400") || strings.Contains(msg, "bad request")) &&
		messageIndicatesContextOverflow(msg)
}

// messageIndicatesContextOverflow reports whether a (lowercased) provider error
// message describes a context-window / token-limit overflow — the kind that
// EmergencyTruncate can actually recover from.
//
// Tightened from the old bare "token"/"maximum" substring match, which
// mis-classified unrelated 400s — a "max_tokens must be <= N" parameter error,
// "maximum number of tools exceeded", etc. — as context overflow and pointlessly
// truncated conversation history (then re-failed on the same non-context error).
// Real overflows reliably say "context", "too long"/"too large", or pair "token"
// with a limit/exceed/maximum qualifier; a bare "maximum" or bare "token" does not.
func messageIndicatesContextOverflow(msg string) bool {
	if strings.Contains(msg, "context") ||
		strings.Contains(msg, "too long") ||
		strings.Contains(msg, "too large") {
		return true
	}
	if strings.Contains(msg, "token") &&
		(strings.Contains(msg, "limit") || strings.Contains(msg, "exceed") ||
			strings.Contains(msg, "maximum") || strings.Contains(msg, "too many")) {
		return true
	}
	return false
}

// ResetClientFallback resets the fallback position on a client if it supports failover.
// This is a no-op for non-FallbackClient instances.
func ResetClientFallback(c Client) {
	type fallbackResetter interface {
		ResetFallbackPosition()
	}
	if fc, ok := c.(fallbackResetter); ok {
		fc.ResetFallbackPosition()
	}
}
