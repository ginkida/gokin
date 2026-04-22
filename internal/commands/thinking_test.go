package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

func newThinkingApp(cfg *config.Config) *fakeAppForAuth {
	return newAuthApp(cfg)
}

// TestThinking_StatusOff_SuggestsEnable verifies the no-args path when
// thinking is disabled — should tell the user exactly how to turn it on.
func TestThinking_StatusOff_SuggestsEnable(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{Name: "kimi-for-coding"},
	})
	cmd := &ThinkingCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "thinking: off") {
		t.Errorf("status should report off: %q", out)
	}
	if !strings.Contains(out, "/thinking on") {
		t.Errorf("status should suggest /thinking on: %q", out)
	}
}

// TestThinking_StatusOn_ShowsBudget verifies the on-path surfaces the budget.
// Users ask "is it actually firing?" — the budget value answers that.
func TestThinking_StatusOn_ShowsBudget(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{
			Name:           "kimi-for-coding",
			EnableThinking: true,
			ThinkingBudget: 4096,
		},
	})
	cmd := &ThinkingCommand{}
	out, err := cmd.Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "thinking: on") {
		t.Errorf("status should report on: %q", out)
	}
	if !strings.Contains(out, "4096 tokens") {
		t.Errorf("status should mention budget: %q", out)
	}
	if !strings.Contains(out, "kimi-for-coding") {
		t.Errorf("status should name current model: %q", out)
	}
}

// TestThinking_UnsupportedModelWarning — users on ollama/llama3.2 who flip
// thinking on will see the setting apply but no reasoning stream. Status
// must flag that so they don't think it's broken.
func TestThinking_UnsupportedModelWarning(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{
			Name:           "llama3.2",
			EnableThinking: true,
			ThinkingBudget: 4096,
		},
	})
	cmd := &ThinkingCommand{}
	out, _ := cmd.Execute(context.Background(), nil, app)
	if !strings.Contains(strings.ToLower(out), "may not emit") {
		t.Errorf("unsupported model status should warn: %q", out)
	}
}

// TestThinking_On_AppliesDefaultBudget: /thinking on with no prior budget
// must not leave budget=0 (which sends a provider-side 400). It auto-fills
// the default so "on" means "actually working".
func TestThinking_On_AppliesDefaultBudget(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{Name: "kimi-for-coding"},
	})
	cmd := &ThinkingCommand{}
	out, err := cmd.Execute(context.Background(), []string{"on"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.applied == nil {
		t.Fatal("ApplyConfig not called")
	}
	if !app.applied.Model.EnableThinking {
		t.Error("EnableThinking should be true after /thinking on")
	}
	if app.applied.Model.ThinkingBudget != thinkingDefaultBudget {
		t.Errorf("budget = %d, want default %d", app.applied.Model.ThinkingBudget, thinkingDefaultBudget)
	}
	if !strings.Contains(out, "on") {
		t.Errorf("output should confirm on: %q", out)
	}
}

// TestThinking_On_PreservesExistingBudget: if the user has already dialed
// in a custom budget, "on" must not clobber it back to default.
func TestThinking_On_PreservesExistingBudget(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{
			Name:           "kimi-for-coding",
			ThinkingBudget: 2048, // user-set, thinking currently off
		},
	})
	cmd := &ThinkingCommand{}
	_, err := cmd.Execute(context.Background(), []string{"on"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.applied.Model.ThinkingBudget != 2048 {
		t.Errorf("existing budget 2048 was clobbered: got %d", app.applied.Model.ThinkingBudget)
	}
}

// TestThinking_Off_Disables: /thinking off must clear the enable flag but
// preserve an existing budget (so /thinking on retains the user's preference
// across toggle cycles).
func TestThinking_Off_Disables(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{
			Name:           "kimi-for-coding",
			EnableThinking: true,
			ThinkingBudget: 4096,
		},
	})
	cmd := &ThinkingCommand{}
	out, err := cmd.Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.applied.Model.EnableThinking {
		t.Error("EnableThinking should be false after /thinking off")
	}
	if app.applied.Model.ThinkingBudget != 4096 {
		t.Errorf("off should not clear existing budget (preserves user preference across toggles): got %d",
			app.applied.Model.ThinkingBudget)
	}
	if !strings.Contains(out, "off") {
		t.Errorf("output should confirm off: %q", out)
	}
}

// TestThinking_Off_FreshConfig_SurvivesRestart: on a fresh config (budget=0),
// /thinking off must persist enough state that factory's auto-enable branch
// doesn't re-enable thinking on the next startup. Factory treats budget==0
// as "never configured" and flips thinking on — so off must seed a non-zero
// budget to signal "user explicitly disabled".
func TestThinking_Off_FreshConfig_SurvivesRestart(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{Name: "kimi-for-coding"}, // fresh: budget=0
	})
	cmd := &ThinkingCommand{}
	_, err := cmd.Execute(context.Background(), []string{"off"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if app.applied.Model.EnableThinking {
		t.Error("EnableThinking should be false")
	}
	// Non-zero budget is the "user touched this" signal for factory.
	if app.applied.Model.ThinkingBudget == 0 {
		t.Error("fresh-config /thinking off saved budget=0 — factory would re-enable on restart")
	}
}

// TestThinking_NumericBudget_SetsAndEnables: passing a number is shorthand
// for "enable thinking at this budget" — setting a budget without enabling
// would be inert.
func TestThinking_NumericBudget_SetsAndEnables(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{Name: "kimi-for-coding"},
	})
	cmd := &ThinkingCommand{}
	out, err := cmd.Execute(context.Background(), []string{"12000"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !app.applied.Model.EnableThinking {
		t.Error("numeric budget should also enable thinking")
	}
	if app.applied.Model.ThinkingBudget != 12000 {
		t.Errorf("budget = %d, want 12000", app.applied.Model.ThinkingBudget)
	}
	if !strings.Contains(out, "12000") {
		t.Errorf("output should echo the new budget: %q", out)
	}
}

// TestThinking_BudgetBounds: values outside [min, max] are rejected with a
// clear message — the provider would 400 on them anyway, we catch it earlier
// with a better error.
func TestThinking_BudgetBounds(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		wantHint string
	}{
		{"below_min", "512", "low"},
		{"zero", "0", "low"},
		{"negative", "-100", "low"},
		{"above_max", "999999", "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newThinkingApp(&config.Config{
				Model: config.ModelConfig{Name: "kimi-for-coding"},
			})
			cmd := &ThinkingCommand{}
			out, _ := cmd.Execute(context.Background(), []string{tc.value}, app)
			if !strings.Contains(strings.ToLower(out), tc.wantHint) {
				t.Errorf("expected %q in rejection: %q", tc.wantHint, out)
			}
			if app.applyCalls != 0 {
				t.Errorf("out-of-range budget must not be persisted")
			}
		})
	}
}

// TestThinking_On_ClampsOutOfRangeBudget: users who hand-edited config.yaml
// to an out-of-range budget (e.g. `thinking_budget: 500`, below the API
// floor) would otherwise see a cryptic 400 from the provider on the next
// message. /thinking on silently repairs those to the default.
func TestThinking_On_ClampsOutOfRangeBudget(t *testing.T) {
	cases := []struct {
		name     string
		existing int32
		want     int32
	}{
		{"below_min", 500, thinkingDefaultBudget},
		{"way_above_max", 200000, thinkingDefaultBudget},
		{"valid_stays", 4096, 4096},
		{"at_min", 1024, 1024},
		{"at_max", 65536, 65536},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newThinkingApp(&config.Config{
				Model: config.ModelConfig{
					Name:           "kimi-for-coding",
					ThinkingBudget: tc.existing,
				},
			})
			cmd := &ThinkingCommand{}
			_, err := cmd.Execute(context.Background(), []string{"on"}, app)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if app.applied.Model.ThinkingBudget != tc.want {
				t.Errorf("budget = %d, want %d (existing was %d)",
					app.applied.Model.ThinkingBudget, tc.want, tc.existing)
			}
		})
	}
}

// TestThinking_UnknownOption_Rejects ensures a typo produces a recognizable
// error rather than silently changing something.
func TestThinking_UnknownOption_Rejects(t *testing.T) {
	app := newThinkingApp(&config.Config{
		Model: config.ModelConfig{Name: "kimi-for-coding"},
	})
	cmd := &ThinkingCommand{}
	out, _ := cmd.Execute(context.Background(), []string{"maybe"}, app)
	if !strings.Contains(strings.ToLower(out), "unknown") {
		t.Errorf("typo should be called out: %q", out)
	}
	if app.applyCalls != 0 {
		t.Errorf("unknown option must not persist")
	}
}

// TestModelSupportsThinking pins the provider model list. Duplicates the
// factory checks — see docstring on modelSupportsThinking.
func TestModelSupportsThinking(t *testing.T) {
	cases := map[string]bool{
		"kimi-for-coding":  true,
		"Kimi-for-Coding":  true,
		"kimi-k2.6":        true,
		"kimi-k2-thinking": true,
		"glm-5.1":          true,
		"glm-4.7":          true,
		"GLM-5.1":          true,
		"glm-4.5":          false, // below 4.7
		"llama3.2":         false,
		"minimax-m2.7":     false,
		"":                 false,
	}
	for model, want := range cases {
		t.Run(model, func(t *testing.T) {
			if got := modelSupportsThinking(model); got != want {
				t.Errorf("modelSupportsThinking(%q) = %v, want %v", model, got, want)
			}
		})
	}
}
