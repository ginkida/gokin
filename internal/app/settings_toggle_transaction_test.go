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
	cfg.UI.ReducedMotion = false
	app := &App{config: cfg, ctx: context.Background()}

	result := app.applySettingToggle("reducedmotion", true)
	if result.Success || result.On || cfg.UI.ReducedMotion {
		t.Fatalf("failed apply was not rolled back: result=%+v config=%v", result, cfg.UI.ReducedMotion)
	}
	if !strings.Contains(result.Message, "previous value restored") {
		t.Fatalf("rollback result lacks explanation: %q", result.Message)
	}
}

func TestApplySettingToggleRejectsUnknownKeyWithoutMutation(t *testing.T) {
	cfg := config.DefaultConfig()
	app := &App{config: cfg, ctx: context.Background()}
	result := app.applySettingToggle("not-a-setting", true)
	if result.Success || !strings.Contains(result.Message, "unknown setting") {
		t.Fatalf("unknown setting result=%+v", result)
	}
}
