package client

import (
	"fmt"
	"testing"
	"time"

	"gokin/internal/config"
)

func newGLMTestConfig() *config.Config {
	return &config.Config{
		API: config.APIConfig{
			GLMKey: "glm-test-key-for-unit-test-123456789",
		},
		Model: config.ModelConfig{
			Name:            "glm-5.2",
			MaxOutputTokens: 65536,
		},
	}
}

func TestSupportsGLMThinking(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"glm-5.2", true},
		{"GLM-5.2", true},
		{"glm-5-turbo", true},
		{"glm-4.7", true},
		{"glm-4.6", false},
		{"glm-4.5", false},
		{"kimi-for-coding", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := supportsGLMThinking(tt.model); got != tt.want {
				t.Fatalf("supportsGLMThinking(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestNewGLMClient_GLM52Defaults(t *testing.T) {
	c, err := newGLMClient(newGLMTestConfig(), "glm-5.2")
	if err != nil {
		t.Fatalf("newGLMClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ac, ok := c.(*AnthropicClient)
	if !ok {
		t.Fatalf("client type = %T, want *AnthropicClient", c)
	}
	if ac.config.Provider != "glm" {
		t.Errorf("Provider = %q, want glm", ac.config.Provider)
	}
	if ac.config.Model != "glm-5.2" {
		t.Errorf("Model = %q, want glm-5.2", ac.config.Model)
	}
	if ac.config.BaseURL != DefaultGLMBaseURL {
		t.Errorf("BaseURL = %q, want %q", ac.config.BaseURL, DefaultGLMBaseURL)
	}
	if !ac.config.EnableThinking {
		t.Error("GLM-5.2 should auto-enable extended thinking")
	}
	if ac.config.ThinkingBudget != defaultGLMThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d", ac.config.ThinkingBudget, defaultGLMThinkingBudget)
	}
	if ac.config.StreamIdleTimeout != 180*time.Second {
		t.Errorf("StreamIdleTimeout = %v, want 3m", ac.config.StreamIdleTimeout)
	}
	if ac.config.HTTPTimeout != 5*time.Minute {
		t.Errorf("HTTPTimeout = %v, want 5m", ac.config.HTTPTimeout)
	}
}

func TestNewGLMClient_PreservesOverrides(t *testing.T) {
	cfg := newGLMTestConfig()
	cfg.Model.EnableThinking = true
	cfg.Model.ThinkingBudget = 16384
	cfg.Model.CustomBaseURL = "https://glm.example.internal/anthropic"
	cfg.API.Retry.Providers = map[string]config.ProviderRetryConfig{
		"glm": {
			StreamIdleTimeout: 4 * time.Minute,
			HTTPTimeout:       9 * time.Minute,
		},
	}

	c, err := newGLMClient(cfg, "glm-5.2")
	if err != nil {
		t.Fatalf("newGLMClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ac := c.(*AnthropicClient)

	if ac.config.ThinkingBudget != 16384 {
		t.Errorf("ThinkingBudget = %d, want configured 16384", ac.config.ThinkingBudget)
	}
	if ac.config.BaseURL != cfg.Model.CustomBaseURL {
		t.Errorf("BaseURL = %q, want custom %q", ac.config.BaseURL, cfg.Model.CustomBaseURL)
	}
	if ac.config.StreamIdleTimeout != 4*time.Minute {
		t.Errorf("StreamIdleTimeout = %v, want 4m", ac.config.StreamIdleTimeout)
	}
	if ac.config.HTTPTimeout != 9*time.Minute {
		t.Errorf("HTTPTimeout = %v, want 9m", ac.config.HTTPTimeout)
	}
}

func TestNewGLMClient_RepairsInvalidThinkingBudget(t *testing.T) {
	for _, budget := range []int32{-1, 100, 65537} {
		t.Run(fmt.Sprintf("budget_%d", budget), func(t *testing.T) {
			cfg := newGLMTestConfig()
			cfg.Model.EnableThinking = true
			cfg.Model.ThinkingBudget = budget
			c, err := newGLMClient(cfg, "glm-5.2")
			if err != nil {
				t.Fatalf("newGLMClient: %v", err)
			}
			t.Cleanup(func() { _ = c.Close() })
			if got := c.(*AnthropicClient).config.ThinkingBudget; got != defaultGLMThinkingBudget {
				t.Errorf("budget %d normalized to %d, want %d", budget, got, defaultGLMThinkingBudget)
			}
		})
	}
}

func TestNewGLMClient_MissingKey(t *testing.T) {
	_, err := newGLMClient(&config.Config{Model: config.ModelConfig{Name: "glm-5.2"}}, "glm-5.2")
	if err == nil {
		t.Fatal("expected a missing GLM key error")
	}
}
