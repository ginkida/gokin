package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestApplyModelSelectionRollsBackWhenClientRebuildFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	oldModel := cfg.Model
	app := &App{config: cfg, ctx: context.Background()}

	result := app.applyModelSelection("provider-that-does-not-exist/model")
	if result.Success || result.ModelID != oldModel.Name {
		t.Fatalf("failed switch result=%+v old=%q", result, oldModel.Name)
	}
	if !reflect.DeepEqual(cfg.Model, oldModel) {
		t.Fatalf("caller config mutated after rollback: got=%+v want=%+v", cfg.Model, oldModel)
	}
	if app.config.Model.Name != oldModel.Name || strings.Contains(app.config.Model.Provider, "does-not-exist") {
		t.Fatalf("app config retained failed selection: got=%+v previous=%+v", app.config.Model, oldModel)
	}
	if !strings.Contains(result.Message, "previous model restored") {
		t.Fatalf("rollback result lacks explanation: %q", result.Message)
	}
}

func TestApplyModelSelectionRejectsEmptyIDWithoutMutation(t *testing.T) {
	cfg := config.DefaultConfig()
	oldModel := cfg.Model
	app := &App{config: cfg, ctx: context.Background()}
	result := app.applyModelSelection("  ")
	if result.Success || !reflect.DeepEqual(cfg.Model, oldModel) || !strings.Contains(result.Message, "empty") {
		t.Fatalf("empty model result=%+v config=%+v", result, cfg.Model)
	}
}
