package client

import (
	"testing"

	"gokin/internal/config"
)

// TestSupportsKimiThinking pins the model-prefix match used to auto-enable
// Extended Thinking for Kimi Coding Plan. Drift here silently turns off
// thinking for every user, so the cases are explicit.
func TestSupportsKimiThinking(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Current Coding Plan model.
		{"kimi-for-coding", true},
		{"Kimi-for-Coding", true}, // case-insensitive
		// Future Coding Plan variants — prefix match guards us against
		// having to touch this function when they ship kimi-k2.7 etc.
		{"kimi-k2.6", true},
		{"kimi-k2-thinking", true},
		// Non-Kimi models must not trip the branch.
		{"glm-5.1", false},
		{"glm-4.7", false},
		{"llama3.2", false},
		{"", false},
		{"random-nonsense", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			if got := supportsKimiThinking(c.model); got != c.want {
				t.Errorf("supportsKimiThinking(%q) = %v, want %v", c.model, got, c.want)
			}
		})
	}
}

// TestNewKimiClient_AutoEnablesThinking confirms the factory flips thinking
// on for a K2.6 user who hasn't configured it — the exact default-flow the
// user complained about. Without this test a future config-refactor could
// silently drop the auto-enable and every new Kimi install would lose the
// reasoning stream.
func TestNewKimiClient_AutoEnablesThinking(t *testing.T) {
	cfg := newKimiTestConfig()
	c, err := newKimiClient(cfg, "kimi-for-coding")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac, ok := c.(*AnthropicClient)
	if !ok {
		t.Fatalf("expected *AnthropicClient, got %T", c)
	}
	if !ac.config.EnableThinking {
		t.Error("EnableThinking should auto-flip to true for kimi-for-coding when user hasn't configured it")
	}
	if ac.config.ThinkingBudget != defaultKimiThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d (auto-default)", ac.config.ThinkingBudget, defaultKimiThinkingBudget)
	}
}

// TestNewKimiClient_RespectsExplicitEnable verifies the auto-enable does not
// override a user who explicitly set EnableThinking=true with their own budget.
func TestNewKimiClient_RespectsExplicitEnable(t *testing.T) {
	cfg := newKimiTestConfig()
	cfg.Model.EnableThinking = true
	cfg.Model.ThinkingBudget = 2048
	c, err := newKimiClient(cfg, "kimi-for-coding")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if !ac.config.EnableThinking {
		t.Error("explicit EnableThinking=true should be preserved")
	}
	if ac.config.ThinkingBudget != 2048 {
		t.Errorf("explicit ThinkingBudget=2048 should be preserved, got %d", ac.config.ThinkingBudget)
	}
}

// TestNewKimiClient_RespectsExplicitBudget guards against the subtle case
// where a user wants to set only a custom budget (indicating they want
// thinking) — the auto-enable branch must not reset their budget to default.
func TestNewKimiClient_RespectsExplicitBudget(t *testing.T) {
	cfg := newKimiTestConfig()
	cfg.Model.ThinkingBudget = 16384 // user wants a big budget
	c, err := newKimiClient(cfg, "kimi-for-coding")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if ac.config.ThinkingBudget != 16384 {
		t.Errorf("explicit budget should win over default: got %d", ac.config.ThinkingBudget)
	}
}

// TestNewKimiClient_DoesNotEnableForUnsupportedModel verifies the prefix
// guard — a config where someone accidentally set model.name to something
// other than a Kimi variant must not get Extended Thinking wired on.
func TestNewKimiClient_DoesNotEnableForUnsupportedModel(t *testing.T) {
	cfg := newKimiTestConfig()
	c, err := newKimiClient(cfg, "some-other-model")
	if err != nil {
		t.Fatalf("newKimiClient: %v", err)
	}
	ac := c.(*AnthropicClient)
	if ac.config.EnableThinking {
		t.Error("thinking should NOT auto-enable for non-Kimi model names")
	}
	if ac.config.ThinkingBudget != 0 {
		t.Errorf("ThinkingBudget should stay 0 for non-Kimi model, got %d", ac.config.ThinkingBudget)
	}
}

// TestNewKimiClient_RepairsOutOfRangeBudget: hand-edited config.yaml with an
// out-of-range thinking_budget must not propagate to the provider (it would
// 400 with a cryptic "budget_tokens out of range"). Factory normalizes to
// the auto-default in that case.
func TestNewKimiClient_RepairsOutOfRangeBudget(t *testing.T) {
	cases := []struct {
		name    string
		existing int32
		want    int32
	}{
		{"way_below_min", 100, defaultKimiThinkingBudget},
		{"at_min_ok", 1024, 1024},
		{"in_range_preserved", 4096, 4096},
		{"at_max_ok", 65536, 65536},
		{"way_above_max", 1_000_000, defaultKimiThinkingBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newKimiTestConfig()
			cfg.Model.EnableThinking = true
			cfg.Model.ThinkingBudget = tc.existing
			c, err := newKimiClient(cfg, "kimi-for-coding")
			if err != nil {
				t.Fatalf("newKimiClient: %v", err)
			}
			ac := c.(*AnthropicClient)
			if ac.config.ThinkingBudget != tc.want {
				t.Errorf("budget = %d, want %d (configured was %d)",
					ac.config.ThinkingBudget, tc.want, tc.existing)
			}
		})
	}
}

// TestNormalizeThinkingBudget nails down the helper's contract separately so
// future provider factories have a single invariant to follow.
func TestNormalizeThinkingBudget(t *testing.T) {
	const def = defaultKimiThinkingBudget
	cases := []struct {
		name   string
		budget int32
		want   int32
	}{
		{"zero_uses_default", 0, def},
		{"below_min_uses_default", 500, def},
		{"negative_uses_default", -1, def},
		{"at_min_preserved", 1024, 1024},
		{"in_range_preserved", 4096, 4096},
		{"at_max_preserved", 65536, 65536},
		{"above_max_uses_default", 100000, def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeThinkingBudget(tc.budget, def); got != tc.want {
				t.Errorf("normalizeThinkingBudget(%d) = %d, want %d", tc.budget, got, tc.want)
			}
		})
	}
}

// newKimiTestConfig returns a minimal config that passes key validation so
// newKimiClient can construct a client without touching the network.
func newKimiTestConfig() *config.Config {
	return &config.Config{
		API: config.APIConfig{
			KimiKey: "sk-kimi-test-key-for-unit-test-12345",
		},
		Model: config.ModelConfig{
			Name:            "kimi-for-coding",
			MaxOutputTokens: 4096,
		},
	}
}
