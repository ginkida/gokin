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
