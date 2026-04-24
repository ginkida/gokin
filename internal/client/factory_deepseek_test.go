package client

import (
	"testing"

	"gokin/internal/config"
)

// newDeepSeekTestConfig mirrors newKimiTestConfig for the deepseek
// factory path — a minimal config that lets newDeepSeekClient build
// without network calls. Key is long enough to pass ValidateKeyFormat.
func newDeepSeekTestConfig() *config.Config {
	return &config.Config{
		API: config.APIConfig{
			DeepSeekKey: "sk-deepseek-test-key-for-unit-test-12345",
		},
		Model: config.ModelConfig{
			Name:            "deepseek-v4-pro",
			MaxOutputTokens: 4096,
		},
	}
}

// TestSupportsDeepSeekThinking pins which DeepSeek models get Extended
// Thinking auto-enabled. V4 (pro + flash) and legacy reasoner: yes.
// deepseek-chat: no — it's a pure chat model and the API rejects
// thinking blocks on that route.
func TestSupportsDeepSeekThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// V4 generation — both variants support thinking.
		{"deepseek-v4-pro", true},
		{"deepseek-v4-flash", true},
		{"DeepSeek-V4-Pro", true}, // case-insensitive
		// Legacy reasoner.
		{"deepseek-reasoner", true},
		// Plain chat must NOT get thinking — endpoint rejects it.
		{"deepseek-chat", false},
		// Bare "deepseek" is a family-level fallback — treat as V4-ish.
		{"deepseek", true},
		// Non-DeepSeek models must not trip.
		{"glm-5.1", false},
		{"kimi-for-coding", false},
		{"", false},
		{"random", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			if got := supportsDeepSeekThinking(c.model); got != c.want {
				t.Errorf("supportsDeepSeekThinking(%q) = %v, want %v", c.model, got, c.want)
			}
		})
	}
}

// TestNewDeepSeekClient_AutoEnablesThinking confirms that a user who
// did not configure thinking still gets it on for V4 Pro. Parallels
// TestNewKimiClient_AutoEnablesThinking.
func TestNewDeepSeekClient_AutoEnablesThinking(t *testing.T) {
	cfg := newDeepSeekTestConfig()
	c, err := newDeepSeekClient(cfg, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("newDeepSeekClient: %v", err)
	}
	ac, ok := c.(*AnthropicClient)
	if !ok {
		t.Fatalf("expected *AnthropicClient, got %T", c)
	}
	if !ac.config.EnableThinking {
		t.Error("EnableThinking should auto-flip to true for deepseek-v4-pro")
	}
	if ac.config.ThinkingBudget != defaultDeepSeekThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d (auto-default)",
			ac.config.ThinkingBudget, defaultDeepSeekThinkingBudget)
	}
	if ac.config.Provider != "deepseek" {
		t.Errorf("Provider = %q, want deepseek", ac.config.Provider)
	}
	if ac.config.BaseURL != DefaultDeepSeekBaseURL {
		t.Errorf("BaseURL = %q, want %q", ac.config.BaseURL, DefaultDeepSeekBaseURL)
	}
}

// TestNewDeepSeekClient_NoThinkingForChat confirms deepseek-chat is
// correctly left without a thinking budget — otherwise API requests
// to that route would 400.
func TestNewDeepSeekClient_NoThinkingForChat(t *testing.T) {
	cfg := newDeepSeekTestConfig()
	cfg.Model.Name = "deepseek-chat"
	c, err := newDeepSeekClient(cfg, "deepseek-chat")
	if err != nil {
		t.Fatalf("newDeepSeekClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if ac.config.EnableThinking {
		t.Error("thinking must NOT auto-enable for deepseek-chat")
	}
	if ac.config.ThinkingBudget != 0 {
		t.Errorf("ThinkingBudget should stay 0 for deepseek-chat, got %d",
			ac.config.ThinkingBudget)
	}
}

// TestNewDeepSeekClient_CustomBaseURLOverrides confirms that a user
// can point at an alternate endpoint (e.g. a corporate proxy or a
// future region-specific mirror) via model.custom_base_url.
func TestNewDeepSeekClient_CustomBaseURLOverrides(t *testing.T) {
	cfg := newDeepSeekTestConfig()
	cfg.Model.CustomBaseURL = "https://proxy.example.internal/anthropic"
	c, err := newDeepSeekClient(cfg, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("newDeepSeekClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if ac.config.BaseURL != "https://proxy.example.internal/anthropic" {
		t.Errorf("custom base URL not honoured: got %q", ac.config.BaseURL)
	}
}

// TestNewDeepSeekClient_MissingKey confirms the factory returns a
// user-friendly error when no API key is configured.
func TestNewDeepSeekClient_MissingKey(t *testing.T) {
	cfg := &config.Config{
		Model: config.ModelConfig{Name: "deepseek-v4-pro"},
	}
	_, err := newDeepSeekClient(cfg, "deepseek-v4-pro")
	if err == nil {
		t.Fatal("expected error for missing DeepSeek key")
	}
}
