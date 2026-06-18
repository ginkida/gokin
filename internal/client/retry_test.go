package client

import (
	"net/http"
	"testing"
	"time"
)

func TestAdaptiveRetryConfigHealthy(t *testing.T) {
	// Setup: record many successes to make provider very healthy
	provider := "test-adaptive-healthy"
	for i := 0; i < 10; i++ {
		recordProviderSuccess(provider)
	}

	cfg := AdaptiveRetryConfig(provider)
	if cfg.RetryDelay >= 1*time.Second {
		t.Errorf("healthy provider should have short delay, got %v", cfg.RetryDelay)
	}
	if cfg.MaxRetries < 10 {
		t.Errorf("healthy provider MaxRetries = %d, want >= 10", cfg.MaxRetries)
	}
}

func TestAdaptiveRetryConfigUnhealthy(t *testing.T) {
	provider := "test-adaptive-unhealthy"
	for i := 0; i < 10; i++ {
		recordProviderFailure(provider, false)
	}

	cfg := AdaptiveRetryConfig(provider)
	if cfg.RetryDelay < 4*time.Second {
		t.Errorf("unhealthy provider should have longer delay, got %v", cfg.RetryDelay)
	}
	if cfg.MaxRetries > 5 {
		t.Errorf("unhealthy provider MaxRetries = %d, want <= 5", cfg.MaxRetries)
	}
}

func TestAdaptiveRetryConfigNewProvider(t *testing.T) {
	cfg := AdaptiveRetryConfig("test-adaptive-unknown-provider")
	defaults := DefaultRetryConfig()
	if cfg.MaxRetries != defaults.MaxRetries {
		t.Errorf("new provider MaxRetries = %d, want default %d", cfg.MaxRetries, defaults.MaxRetries)
	}
}

func TestAdaptiveStreamRetryPolicyHealthy(t *testing.T) {
	provider := "test-stream-healthy"
	for i := 0; i < 10; i++ {
		recordProviderSuccess(provider)
	}

	policy := AdaptiveStreamRetryPolicy(provider)
	if policy.MaxRetries < 3 {
		t.Errorf("healthy provider stream MaxRetries = %d, want >= 3", policy.MaxRetries)
	}
}

func TestAdaptiveStreamRetryPolicyUnhealthy(t *testing.T) {
	provider := "test-stream-unhealthy"
	for i := 0; i < 15; i++ {
		recordProviderFailure(provider, false)
	}

	policy := AdaptiveStreamRetryPolicy(provider)
	if policy.MaxRetries > 1 {
		t.Errorf("unhealthy provider stream MaxRetries = %d, want <= 1", policy.MaxRetries)
	}
	if policy.BaseDelay < 5*time.Second {
		t.Errorf("unhealthy provider BaseDelay = %v, want >= 5s", policy.BaseDelay)
	}
}

func TestAdaptiveStreamRetryPolicyKimiDefaults(t *testing.T) {
	policy := AdaptiveStreamRetryPolicy("kimi")
	if policy.MaxRetries < 3 {
		t.Errorf("kimi MaxRetries = %d, want >= 3", policy.MaxRetries)
	}
	if policy.MaxPartialRetries < 2 {
		t.Errorf("kimi MaxPartialRetries = %d, want >= 2", policy.MaxPartialRetries)
	}
	if policy.BaseDelay < 3*time.Second {
		t.Errorf("kimi BaseDelay = %v, want >= 3s", policy.BaseDelay)
	}
	if policy.MaxDelay < 45*time.Second {
		t.Errorf("kimi MaxDelay = %v, want >= 45s", policy.MaxDelay)
	}
}

// TestAdaptiveStreamRetryPolicyGLMDefaults pins that GLM — the default provider,
// which stalls mid-stream the same way kimi does ("stream idle after partial
// response") — gets the same extra cold + partial retry budget.
func TestAdaptiveStreamRetryPolicyGLMDefaults(t *testing.T) {
	policy := AdaptiveStreamRetryPolicy("glm")
	if policy.MaxRetries < 3 {
		t.Errorf("glm MaxRetries = %d, want >= 3", policy.MaxRetries)
	}
	if policy.MaxPartialRetries < 2 {
		t.Errorf("glm MaxPartialRetries = %d, want >= 2 (GLM stalls mid-stream like kimi)", policy.MaxPartialRetries)
	}
}

// TestDecideStreamRetryPartialGLMGetsTwoRetries pins the end-to-end effect: a
// GLM partial stream-idle (the user-reported "function response error
// (stream_idle_timeout)") is retried TWICE before surfacing, not once.
func TestDecideStreamRetryPartialGLMGetsTwoRetries(t *testing.T) {
	policy := AdaptiveStreamRetryPolicy("glm")
	partialErr := &ErrStreamIdleTimeout{Timeout: 180 * time.Second, Partial: true}
	opts := StreamRetryOptions{AllowPartial: true}

	for _, used := range []int{0, 1} {
		d := DecideStreamRetry(policy, partialErr, 0, used, nil, opts)
		if !d.ShouldRetry {
			t.Errorf("GLM partial stall with %d partial retries used should still retry (budget %d)", used, policy.MaxPartialRetries)
		}
		if !d.Partial {
			t.Errorf("a partial stream-idle decision should be marked Partial")
		}
	}
	if d := DecideStreamRetry(policy, partialErr, 0, 2, nil, opts); d.ShouldRetry {
		t.Errorf("GLM partial stall should stop after %d partial retries", policy.MaxPartialRetries)
	}
}

func TestAdaptiveRetryConfigFailureStreak(t *testing.T) {
	provider := "test-adaptive-streak"
	// Make it slightly healthy first
	for i := 0; i < 3; i++ {
		recordProviderSuccess(provider)
	}
	// Then 5 consecutive failures
	for i := 0; i < 5; i++ {
		recordProviderFailure(provider, true)
	}

	cfg := AdaptiveRetryConfig(provider)
	if cfg.RetryDelay < 5*time.Second {
		t.Errorf("high streak provider should have delay >= 5s, got %v", cfg.RetryDelay)
	}
	if cfg.MaxRetries > 4 {
		t.Errorf("high streak MaxRetries = %d, want <= 4", cfg.MaxRetries)
	}
}

func TestCalculateBackoffBasic(t *testing.T) {
	delay := CalculateBackoff(1*time.Second, 0, 30*time.Second)
	if delay < 1*time.Second || delay > 2*time.Second {
		t.Errorf("attempt 0 delay = %v, want ~1-1.25s", delay)
	}

	delay2 := CalculateBackoff(1*time.Second, 3, 30*time.Second)
	if delay2 < 8*time.Second {
		t.Errorf("attempt 3 delay = %v, want >= 8s", delay2)
	}
}

func TestCalculateBackoffMaxCap(t *testing.T) {
	delay := CalculateBackoff(1*time.Second, 10, 5*time.Second)
	if delay > 7*time.Second { // 5s + 25% jitter
		t.Errorf("should be capped at ~5s+jitter, got %v", delay)
	}
}

// CalculateBackoff is exported and must never panic or bypass the cap on hostile
// inputs. Before hardening: a large attempt overflowed 1<<attempt to a
// negative/zero delay → rand.Int63n(<=0) PANIC and the maxDelay cap was skipped;
// a negative attempt wrapped uint() to a huge shift → 0 delay → same panic; a
// zero baseDelay → 0 delay → same panic.
func TestCalculateBackoffHostileInputsNoPanicNoCapBypass(t *testing.T) {
	maxDelay := 30 * time.Second
	upper := maxDelay + maxDelay/4 + time.Second // cap + 25% jitter, with slack

	cases := []struct {
		name      string
		baseDelay time.Duration
		attempt   int
	}{
		{"huge attempt overflows the shift", time.Second, 200},
		{"attempt at int64 sign bit", time.Second, 63},
		{"attempt past machine word", time.Second, 1000},
		{"negative attempt wraps uint", time.Second, -5},
		{"zero baseDelay", 0, 3},
		{"negative baseDelay", -1 * time.Second, 3},
		{"tiny baseDelay, low attempt", time.Nanosecond, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The call itself must not panic (rand.Int63n on n<=0).
			delay := CalculateBackoff(c.baseDelay, c.attempt, maxDelay)
			// And it must never exceed the cap (+ jitter) — overflow must not
			// produce a negative/garbage delay that slips past maxDelay.
			if delay < 0 {
				t.Errorf("negative delay %v — overflow not guarded", delay)
			}
			if delay > upper {
				t.Errorf("delay %v exceeds cap+jitter %v — maxDelay bypassed", delay, upper)
			}
		})
	}
}

func TestParseHeaderInt64(t *testing.T) {
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("x-limit", "100")
	resp.Header.Set("x-empty", "")
	resp.Header.Set("x-not-num", "abc")

	if val := ParseHeaderInt64(resp, "x-limit"); val != 100 {
		t.Errorf("ParseHeaderInt64: got %d, want 100", val)
	}
	if val := ParseHeaderInt64(resp, "x-empty"); val != 0 {
		t.Errorf("ParseHeaderInt64 (empty): got %d, want 0", val)
	}
	if val := ParseHeaderInt64(resp, "x-not-num"); val != 0 {
		t.Errorf("ParseHeaderInt64 (invalid): got %d, want 0", val)
	}
}

func TestParseHeaderDuration(t *testing.T) {
	resp := &http.Response{
		Header: make(http.Header),
	}

	// Test cases: Go duration, Bare seconds, Anthropic style
	tests := []struct {
		val  string
		want time.Duration
	}{
		{"1s", time.Second},
		{"45", 45 * time.Second},
		{"45.5", 45500 * time.Millisecond},
		{"1m30s", 90 * time.Second},
		{"", 0},
		{"abc", 0},
	}

	for _, tt := range tests {
		resp.Header.Set("x-dur", tt.val)
		if got := ParseHeaderDuration(resp, "x-dur"); got != tt.want {
			t.Errorf("ParseHeaderDuration(%s): got %v, want %v", tt.val, got, tt.want)
		}
	}
}

func TestExtractAnthropicRateLimits(t *testing.T) {
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("anthropic-ratelimit-requests-limit", "1000")
	resp.Header.Set("anthropic-ratelimit-requests-remaining", "999")
	resp.Header.Set("anthropic-ratelimit-requests-reset", "2026-04-07T20:00:00Z") // Note: my parser handles seconds/durations, checking if it ignores ISO
	resp.Header.Set("anthropic-ratelimit-tokens-limit", "100000")

	rl := extractAnthropicRateLimits(resp)
	if rl == nil {
		t.Fatal("extractAnthropicRateLimits returned nil")
	}
	if rl.RequestsLimit != 1000 || rl.RequestsRemaining != 999 || rl.TokensLimit != 100000 {
		t.Errorf("Unexpected values: %+v", rl)
	}
}

func TestExtractOpenAIRateLimits(t *testing.T) {
	resp := &http.Response{
		Header: make(http.Header),
	}
	resp.Header.Set("x-ratelimit-limit-requests", "200")
	resp.Header.Set("x-ratelimit-remaining-tokens", "50000")
	resp.Header.Set("x-ratelimit-reset-requests", "1.5s")

	rl := extractOpenAIRateLimits(resp)
	if rl == nil {
		t.Fatal("extractOpenAIRateLimits returned nil")
	}
	if rl.RequestsLimit != 200 || rl.TokensRemaining != 50000 || rl.RequestsReset != 1500*time.Millisecond {
		t.Errorf("Unexpected values: %+v", rl)
	}
}

// TestCappedRetryDelay pins the v0.85.15 fix: a server-supplied Retry-After is
// honored above the computed backoff but never beyond maxRetryAfter, so a bad
// provider sending an absurd value (e.g. a unix timestamp) can't park the
// client for decades, bypassing the backoff cap.
func TestCappedRetryDelay(t *testing.T) {
	const maxRA = 2 * time.Minute
	backoff := 5 * time.Second

	cases := []struct {
		name       string
		retryAfter time.Duration
		want       time.Duration
	}{
		{"absent (0) → backoff", 0, backoff},
		{"negative → backoff", -10 * time.Second, backoff},
		{"below backoff → backoff", 2 * time.Second, backoff},
		{"above backoff, under cap → honored", 30 * time.Second, 30 * time.Second},
		{"absurd (decades) → capped", 1735689600 * time.Second, maxRA},
		{"exactly cap → cap", maxRA, maxRA},
	}
	for _, c := range cases {
		if got := cappedRetryDelay(backoff, c.retryAfter, maxRA); got != c.want {
			t.Errorf("%s: cappedRetryDelay(%v, %v, %v) = %v, want %v",
				c.name, backoff, c.retryAfter, maxRA, got, c.want)
		}
	}
}
