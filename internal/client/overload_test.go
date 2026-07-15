package client

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIsOverloadError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// Current documented GLM transient codes as produced by the classifier.
		{"glm 1305 overloaded", errors.New("GLM server overloaded — retrying [overloaded] (1305): [1305][The server is busy]"), true},
		{"glm 1302 rate limit", errors.New("GLM rate limit — retrying [rate limit] (1302): ..."), true},
		{"anthropic overloaded_error", errors.New("API error 529: overloaded_error"), true},
		{"anthropic rate_limit_error", errors.New("429 rate_limit_error: too many tokens"), true},
		{"too many requests", errors.New("HTTP 429: too many requests"), true},
		{"wrapped overload", errors.New("model response error (other): GLM server overloaded — retrying"), true},

		// Must NOT be treated as overload — these are terminal or unrelated.
		{"glm 1308 quota", errors.New("GLM quota/balance exhausted — top up your GLM plan or switch provider with /provider"), false},
		{"glm 1214 auth", errors.New("GLM authentication failed — check API key"), false},
		{"glm 1301 sensitive content", errors.New("GLM rejected sensitive or unsafe content (1301)"), false},
		{"glm 1210 invalid parameter", errors.New("GLM request has an invalid API parameter (1210)"), false},
		{"context too long", errors.New("400: context length exceeded maximum"), false},
		{"generic timeout", errors.New("request timeout after 1305ms"), false},
		{"connection refused", errors.New("dial tcp: connection refused"), false},
		{"plain text", errors.New("file not found"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOverloadError(tc.err); got != tc.want {
				t.Fatalf("IsOverloadError(%q) = %v, want %v", errStr(tc.err), got, tc.want)
			}
		})
	}
}

func TestIsTransientProviderError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		// Overload class (delegates to IsOverloadError).
		{"glm overloaded", errors.New("GLM server overloaded — retrying [overloaded] (1305)"), true},
		{"rate limit", errors.New("429 rate_limit_error"), true},
		// Network faults.
		{"connection refused", errors.New("dial tcp: connection refused"), true},
		{"connection reset", errors.New("read: connection reset by peer"), true},
		{"no such host", errors.New("lookup api.z.ai: no such host"), true},
		{"service unavailable", errors.New("503 service unavailable"), true},
		{"bad gateway", errors.New("502 bad gateway"), true},
		{"temporarily", errors.New("name resolution failed temporarily"), true},

		// Ambiguous / task-level — must NOT be transient (could be the agent's
		// own long task overrunning, which should count as a task failure).
		{"bare timeout", errors.New("request timeout"), false},
		{"deadline exceeded", errors.New("context deadline exceeded"), false},
		{"EOF", errors.New("unexpected EOF"), false},
		{"quota", errors.New("GLM quota/balance exhausted — top up"), false},
		{"auth", errors.New("authentication failed — check API key"), false},
		{"plain", errors.New("compile error: undefined symbol"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientProviderError(tc.err); got != tc.want {
				t.Fatalf("IsTransientProviderError(%q) = %v, want %v", errStr(tc.err), got, tc.want)
			}
		})
	}
}

func errStr(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestDecideOverloadRetry_NonOverloadDoesNotRetry(t *testing.T) {
	d := DecideOverloadRetry(DefaultOverloadRetryPolicy(), errors.New("auth failed"), 0, 0, context.Background())
	if d.ShouldRetry {
		t.Fatalf("non-overload error must not take the patient budget: %+v", d)
	}
	// nil error too
	if DecideOverloadRetry(DefaultOverloadRetryPolicy(), nil, 0, 0, context.Background()).ShouldRetry {
		t.Fatal("nil error must not retry")
	}
}

func TestDecideOverloadRetry_RetriesPatiently(t *testing.T) {
	overloadErr := errors.New("GLM server overloaded — retrying [overloaded] (1305)")
	policy := DefaultOverloadRetryPolicy()

	d := DecideOverloadRetry(policy, overloadErr, 0, 0, context.Background())
	if !d.ShouldRetry {
		t.Fatal("first overload attempt should retry")
	}
	if d.Reason != "overloaded" {
		t.Fatalf("reason = %q, want overloaded", d.Reason)
	}
	if d.Delay <= 0 || d.Delay > policy.MaxDelay+policy.MaxDelay/4 {
		t.Fatalf("delay %v out of expected range (cap %v)", d.Delay, policy.MaxDelay)
	}

	// Backoff should be generous: still retrying deep into the attempt count,
	// where the small generic stream policy (3 retries) would have given up.
	if !DecideOverloadRetry(policy, overloadErr, 10, 30*time.Second, context.Background()).ShouldRetry {
		t.Fatal("overload should still retry at attempt 10 / 30s elapsed")
	}
}

func TestDecideOverloadRetry_BoundedByMaxRetries(t *testing.T) {
	overloadErr := errors.New("overloaded")
	policy := OverloadRetryPolicy{MaxRetries: 5, BaseDelay: time.Second, MaxDelay: 2 * time.Second, MaxTotal: time.Hour}
	if DecideOverloadRetry(policy, overloadErr, 5, 0, context.Background()).ShouldRetry {
		t.Fatal("must stop once MaxRetries reached")
	}
	if !DecideOverloadRetry(policy, overloadErr, 4, 0, context.Background()).ShouldRetry {
		t.Fatal("must still retry just below MaxRetries")
	}
}

func TestDecideOverloadRetry_BoundedByMaxTotal(t *testing.T) {
	overloadErr := errors.New("overloaded")
	policy := OverloadRetryPolicy{MaxRetries: 1000, BaseDelay: time.Second, MaxDelay: 30 * time.Second, MaxTotal: 60 * time.Second}
	if DecideOverloadRetry(policy, overloadErr, 0, 60*time.Second, context.Background()).ShouldRetry {
		t.Fatal("must stop once total budget is spent")
	}
	// Near the budget edge, the next delay is trimmed so it can't overshoot.
	d := DecideOverloadRetry(policy, overloadErr, 0, 55*time.Second, context.Background())
	if !d.ShouldRetry {
		t.Fatal("should still retry with budget remaining")
	}
	if d.Delay > 5*time.Second {
		t.Fatalf("final delay %v overshoots remaining budget (5s)", d.Delay)
	}
}

func TestDecideOverloadRetry_RespectsContextCancellation(t *testing.T) {
	overloadErr := errors.New("overloaded")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if DecideOverloadRetry(DefaultOverloadRetryPolicy(), overloadErr, 0, 0, ctx).ShouldRetry {
		t.Fatal("cancelled context must not retry")
	}
}

func TestDecideOverloadRetry_ZeroPolicyUsesDefaults(t *testing.T) {
	// A zero-value policy must normalize to the defaults, not deadlock at 0 budget.
	d := DecideOverloadRetry(OverloadRetryPolicy{}, errors.New("overloaded"), 0, 0, context.Background())
	if !d.ShouldRetry || d.Delay <= 0 {
		t.Fatalf("zero policy should normalize and retry, got %+v", d)
	}
}
