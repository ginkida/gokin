package client

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestAPIError(t *testing.T) {
	err := &APIError{StatusCode: 429, Message: "rate limited"}
	if err.Error() != "API error 429: rate limited" {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestErrStreamIdleTimeout(t *testing.T) {
	err := &ErrStreamIdleTimeout{Timeout: 30 * time.Second, Partial: false}
	if !containsLower(err.Error(), "no data received") {
		t.Errorf("non-partial error = %q", err.Error())
	}

	err = &ErrStreamIdleTimeout{Timeout: 30 * time.Second, Partial: true}
	if !containsLower(err.Error(), "partial response") {
		t.Errorf("partial error = %q", err.Error())
	}
}

func TestIsStreamIdleTimeout(t *testing.T) {
	sit := &ErrStreamIdleTimeout{Timeout: 30 * time.Second}
	if !IsStreamIdleTimeout(sit) {
		t.Error("should detect ErrStreamIdleTimeout")
	}
	if !IsStreamIdleTimeout(fmt.Errorf("wrapped: %w", sit)) {
		t.Error("should detect wrapped ErrStreamIdleTimeout")
	}
	if IsStreamIdleTimeout(errors.New("other error")) {
		t.Error("should not detect plain error")
	}
	if IsStreamIdleTimeout(nil) {
		t.Error("should not detect nil")
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context.Canceled", context.Canceled, false},
		{"model round timeout", ErrModelRoundTimeout, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"stream idle timeout", &ErrStreamIdleTimeout{Timeout: 30 * time.Second}, true},
		{"API 429", &APIError{StatusCode: 429}, true},
		{"API 503", &APIError{StatusCode: 503}, true},
		{"API 502", &APIError{StatusCode: 502}, true},
		{"API 504", &APIError{StatusCode: 504}, true},
		{"API 400", &APIError{StatusCode: 400}, false},
		{"API 401", &APIError{StatusCode: 401}, false},
		{"rate limit string", errors.New("Rate Limit exceeded"), true},
		{"eof string", errors.New("unexpected EOF"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRetryableAPIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"429", &APIError{StatusCode: 429}, true},
		{"502", &APIError{StatusCode: 502}, true},
		{"503", &APIError{StatusCode: 503}, true},
		{"504", &APIError{StatusCode: 504}, true},
		{"400", &APIError{StatusCode: 400}, false},
		{"401", &APIError{StatusCode: 401}, false},
		{"500", &APIError{StatusCode: 500}, false},
		{"MiniMax model_not_found 400", &HTTPError{StatusCode: 400, Message: "model_not_found error"}, true},
		{"regular 400", &HTTPError{StatusCode: 400, Message: "bad request"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryableAPIError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableAPIError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsRateLimitError(t *testing.T) {
	if IsRateLimitError(nil) {
		t.Error("nil should not be rate limit")
	}
	if !IsRateLimitError(&APIError{StatusCode: 429}) {
		t.Error("API 429 should be rate limit")
	}
	if !IsRateLimitError(&HTTPError{StatusCode: 429}) {
		t.Error("HTTP 429 should be rate limit")
	}
	if !IsRateLimitError(errors.New("rate limit exceeded")) {
		t.Error("string rate limit should be detected")
	}
	if !IsRateLimitError(errors.New("Too Many Requests")) {
		t.Error("too many requests should be detected")
	}
	if IsRateLimitError(errors.New("plain error")) {
		t.Error("plain error should not be rate limit")
	}
}

func TestIsContextTooLongError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"HTTP 400 context", &HTTPError{StatusCode: 400, Message: "context window exceeded"}, true},
		{"HTTP 400 token", &HTTPError{StatusCode: 400, Message: "token limit exceeded"}, true},
		{"HTTP 400 too long", &HTTPError{StatusCode: 400, Message: "input too long"}, true},
		{"HTTP 400 unrelated", &HTTPError{StatusCode: 400, Message: "invalid parameter"}, false},
		{"HTTP 500", &HTTPError{StatusCode: 500, Message: "context"}, false},
		{"API 400 context", &APIError{StatusCode: 400, Message: "maximum context length"}, true},
		{"API 400 unrelated", &APIError{StatusCode: 400, Message: "invalid model"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsContextTooLongError(tt.err)
			if got != tt.want {
				t.Errorf("IsContextTooLongError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectFailureTelemetry(t *testing.T) {
	// Stream idle timeout
	ft := DetectFailureTelemetry(&ErrStreamIdleTimeout{Timeout: 30 * time.Second, Partial: true})
	if ft.Reason != "stream_idle_timeout" {
		t.Errorf("Reason = %q, want stream_idle_timeout", ft.Reason)
	}
	if !ft.Partial {
		t.Error("should be Partial")
	}

	// Model round timeout
	ft = DetectFailureTelemetry(ErrModelRoundTimeout)
	if ft.Reason != "model_round_timeout" {
		t.Errorf("Reason = %q, want model_round_timeout", ft.Reason)
	}

	// Context canceled
	ft = DetectFailureTelemetry(context.Canceled)
	if ft.Reason != "context_cancel" {
		t.Errorf("Reason = %q, want context_cancel", ft.Reason)
	}

	// Nil
	ft = DetectFailureTelemetry(nil)
	if ft.Reason != "other" {
		t.Errorf("Reason = %q, want other", ft.Reason)
	}

	// TypedTimeoutError
	ft = DetectFailureTelemetry(&TimeoutError{
		Reason:   FailureReasonHTTPTimeout,
		Provider: "gemini",
		Timeout:  120 * time.Second,
		Err:      context.DeadlineExceeded,
	})
	if ft.Reason != "http_timeout" {
		t.Errorf("Reason = %q, want http_timeout", ft.Reason)
	}
	if ft.Provider != "gemini" {
		t.Errorf("Provider = %q, want gemini", ft.Provider)
	}
}

func TestTimeoutErrorUnwrap(t *testing.T) {
	inner := errors.New("inner error")
	te := &TimeoutError{Reason: FailureReasonHTTPTimeout, Err: inner}
	if !errors.Is(te, inner) {
		t.Error("should unwrap to inner error")
	}
}

func TestContextErr(t *testing.T) {
	if ContextErr(context.Background()) != nil {
		t.Error("non-cancelled context should return nil")
	}
	// ContextErr with nil is tested via the function signature accepting nil

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ContextErr(ctx) == nil {
		t.Error("cancelled context should return non-nil")
	}
}
