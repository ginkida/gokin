package client

import (
	"testing"
	"time"

	"gokin/internal/config"
)

func TestEffectiveProviderTimeoutsUsesProviderDefaults(t *testing.T) {
	tests := []struct {
		provider string
		headers  time.Duration
		idle     time.Duration
	}{
		{provider: "glm", headers: 5 * time.Minute, idle: 3 * time.Minute},
		{provider: "kimi", headers: 5 * time.Minute, idle: 3 * time.Minute},
		{provider: "minimax", headers: 5 * time.Minute, idle: 2 * time.Minute},
		{provider: "deepseek", headers: 5 * time.Minute, idle: 2 * time.Minute},
		{provider: "ollama", headers: config.DefaultHTTPTimeout, idle: 0},
		{provider: "unknown", headers: config.DefaultHTTPTimeout, idle: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := EffectiveProviderTimeouts(nil, tt.provider)
			if got.ResponseHeaderTimeout != tt.headers || got.StreamIdleTimeout != tt.idle {
				t.Fatalf("timeouts = %+v, want headers=%v idle=%v", got, tt.headers, tt.idle)
			}
		})
	}
}

func TestEffectiveProviderTimeoutsAppliesGlobalThenProviderOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.API.Retry.HTTPTimeout = 90 * time.Second
	cfg.API.Retry.StreamIdleTimeout = 45 * time.Second
	cfg.API.Retry.Providers = map[string]config.ProviderRetryConfig{
		"kimi": {
			HTTPTimeout:       7 * time.Minute,
			StreamIdleTimeout: 3 * time.Minute,
		},
	}

	kimi := EffectiveProviderTimeouts(cfg, " KIMI ")
	if kimi.ResponseHeaderTimeout != 7*time.Minute || kimi.StreamIdleTimeout != 3*time.Minute {
		t.Fatalf("Kimi override = %+v", kimi)
	}
	glm := EffectiveProviderTimeouts(cfg, "glm")
	if glm.ResponseHeaderTimeout != 90*time.Second || glm.StreamIdleTimeout != 45*time.Second {
		t.Fatalf("GLM global override = %+v", glm)
	}
}
