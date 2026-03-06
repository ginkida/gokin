package client

import (
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
