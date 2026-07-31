package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/tools"
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
	executor := tools.NewExecutor(nil, nil, time.Second)
	app := &App{config: cfg, ctx: context.Background(), executor: executor}

	tests := []struct {
		key   string
		on    bool
		check func(*config.Config) bool
	}{
		{"reducedmotion", true, func(c *config.Config) bool { return c.UI.ReducedMotion }},
		{"hints", false, func(c *config.Config) bool { return !c.UI.HintsEnabled }},
		{"toolcalls", false, func(c *config.Config) bool { return !c.UI.ShowToolCalls }},
		{"markdown", false, func(c *config.Config) bool { return !c.UI.MarkdownRendering }},
		{"bell", false, func(c *config.Config) bool { return !c.UI.Bell }},
		{"nativealerts", true, func(c *config.Config) bool { return c.UI.NativeNotifications }},
	}
	for _, tt := range tests {
		result := app.applySettingToggle("", tt.key, tt.on)
		committed := app.GetConfig()
		if !result.Success || result.On != tt.on || committed == nil || !tt.check(committed) {
			t.Fatalf("%s incorrectly depended on provider rebuild: result=%+v config=%v", tt.key, result, committed)
		}
	}
	if !executor.GetNotificationManager().NativeNotificationsEnabled() {
		t.Fatal("nativealerts updated config but not the live NotificationManager")
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
