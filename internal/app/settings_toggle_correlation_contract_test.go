package app

import (
	"context"
	"testing"

	"gokin/internal/config"
)

func TestApplySettingTogglePreservesRequestOwnership(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.UI.ReducedMotion = false
	app := &App{config: cfg, ctx: context.Background()}

	result := app.applySettingToggle("setting-17", "reducedmotion", true)
	if !result.Success || !result.On || result.RequestID != "setting-17" {
		t.Fatalf("setting result lost request ownership: %+v", result)
	}
}
