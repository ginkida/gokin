package client

import (
	"testing"

	"gokin/internal/config"
)

// newDeepSeekTestConfig lets newDeepSeekClient build
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
// Thinking auto-enabled. Saved retired IDs are resolved separately.
func TestSupportsDeepSeekThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// V4 generation — both variants support thinking.
		{"deepseek-v4-pro", true},
		{"deepseek-v4-flash", true},
		{"deepseek-flash", true},
		{"DeepSeek-V4-Pro", true}, // case-insensitive
		// Legacy reasoner.
		{"deepseek-reasoner", true},
		// The raw retired chat ID is not auto-enabled by this gate.
		{"deepseek-chat", false},
		// Bare "deepseek" is a family-level fallback — treat as V4-ish.
		{"deepseek", true},
		// Non-DeepSeek models must not trip.
		{"glm-5.1", false},

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

func TestNewClient_DetectsDeepSeekProviderFromModelName(t *testing.T) {
	cfg := newDeepSeekTestConfig()
	cfg.API.ActiveProvider = "glm"
	cfg.Model.Provider = ""

	pool := GetPool(cfg)
	pool.FlushProvider("deepseek")
	t.Cleanup(func() {
		pool.FlushProvider("deepseek")
	})

	c, err := NewClient(t.Context(), cfg, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
	})

	deepSeekFingerprint := clientConfigFingerprint(cfg, "deepseek", "deepseek-v4-pro")
	if _, ok := pool.Get("deepseek", "deepseek-v4-pro", deepSeekFingerprint); !ok {
		t.Fatal("NewClient should cache inferred DeepSeek client under provider key deepseek")
	}
	glmFingerprint := clientConfigFingerprint(cfg, "glm", "deepseek-v4-pro")
	if _, ok := pool.Get("glm", "deepseek-v4-pro", glmFingerprint); ok {
		t.Fatal("NewClient should not cache DeepSeek model under stale active provider glm")
	}
}

// TestNewDeepSeekClient_AutoEnablesThinking confirms that a user who
// did not configure thinking still gets it on for V4 Pro.
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

func TestNewClient_RetiredDeepSeekModelsUseCurrentFlash(t *testing.T) {
	for _, model := range []string{"deepseek-chat", "deepseek-reasoner"} {
		cfg := newDeepSeekTestConfig()
		cfg.Model.Provider = "deepseek"
		cfg.Model.Name = model
		c, err := NewClient(t.Context(), cfg, model)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if c.GetModel() != "deepseek-flash" || cfg.Model.Name != "deepseek-flash" {
			t.Fatalf("retired %s resolved to client=%q config=%q", model, c.GetModel(), cfg.Model.Name)
		}
		if !c.(*AnthropicClient).config.EnableThinking {
			t.Fatal("Flash must support thinking")
		}
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

// DeepSeek accepts (and ignores) cache_control; caching is implicit.
func TestAnthropicClient_SupportsPromptCachingForDeepSeek(t *testing.T) {
	cases := []struct {
		baseURL string
		want    bool
	}{
		// DeepSeek endpoints — must all pass the gate.
		{"https://api.deepseek.com/anthropic", true},
		{"https://api.deepseek.com/anthropic/v1/messages", true},

		// Native Anthropic.
		{DefaultAnthropicBaseURL, true},
		{"", true}, // empty BaseURL defaults to Anthropic
		// GLM is known not to honour cache_control — still gated off.
		{"https://api.z.ai/api/anthropic", false},
		// Unknown provider — conservative off.
		{"https://random.example.com/api", false},
	}
	for _, c := range cases {
		t.Run(c.baseURL, func(t *testing.T) {
			ac := &AnthropicClient{config: AnthropicConfig{BaseURL: c.baseURL}}
			if got := ac.supportsPromptCaching(); got != c.want {
				t.Errorf("supportsPromptCaching(BaseURL=%q) = %v, want %v",
					c.baseURL, got, c.want)
			}
		})
	}
}
