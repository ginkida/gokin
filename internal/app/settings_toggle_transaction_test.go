package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestApplySettingToggleRollsBackWhenRuntimeApplyFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "provider-that-does-not-exist"
	cfg.API.Backend = "provider-that-does-not-exist"
	cfg.Model.ThinkingMode = config.ThinkingModeAuto
	app := &App{config: cfg, ctx: context.Background()}

	result := app.applySettingToggle("", "thinking", true)
	committed := app.GetConfig()
	if result.Success || result.On || config.ResolveThinkingMode(cfg.Model.ThinkingMode) != config.ThinkingModeAuto ||
		committed == nil || config.ResolveThinkingMode(committed.Model.ThinkingMode) != config.ThinkingModeAuto {
		t.Fatalf("failed apply was not rolled back: result=%+v original=%v committed=%+v", result, cfg.Model.ThinkingMode, committed)
	}
	if !strings.Contains(result.Message, "current value preserved") {
		t.Fatalf("failed apply result lacks authoritative-state explanation: %q", result.Message)
	}
}

func TestApplySettingToggleUIOnlyDoesNotRequireValidProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.API.ActiveProvider = "provider-that-does-not-exist"
	cfg.API.Backend = "provider-that-does-not-exist"
	cfg.UI.ReducedMotion = false
	app := &App{config: cfg, ctx: context.Background()}

	result := app.applySettingToggle("", "reducedmotion", true)
	committed := app.GetConfig()
	if !result.Success || !result.On || committed == nil || !committed.UI.ReducedMotion {
		t.Fatalf("UI-only setting incorrectly depended on provider rebuild: result=%+v config=%v", result, committed)
	}
}

func TestApplySettingToggleRejectsUnknownKeyWithoutMutation(t *testing.T) {
	cfg := config.DefaultConfig()
	app := &App{config: cfg, ctx: context.Background()}
	result := app.applySettingToggle("", "not-a-setting", true)
	if result.Success || !strings.Contains(result.Message, "unknown setting") {
		t.Fatalf("unknown setting result=%+v", result)
	}
}
