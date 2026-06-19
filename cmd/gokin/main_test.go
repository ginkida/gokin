package main

import (
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestApplyRuntimeOverrides_ProviderSelectsRuntimeAndDefaultModel(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Model.Name = "stale-model"

	if err := applyRuntimeOverrides(cfg, "glm", ""); err != nil {
		t.Fatalf("applyRuntimeOverrides() error = %v", err)
	}

	if cfg.API.ActiveProvider != "glm" || cfg.API.Backend != "glm" || cfg.Model.Provider != "glm" {
		t.Fatalf("provider not applied: api=%q backend=%q model.provider=%q", cfg.API.ActiveProvider, cfg.API.Backend, cfg.Model.Provider)
	}
	if cfg.Model.Name != "glm-5.2" {
		t.Fatalf("model name = %q, want provider default glm-5.2", cfg.Model.Name)
	}
}

func TestApplyRuntimeOverrides_ProviderAndModelUsesExplicitModel(t *testing.T) {
	cfg := config.DefaultConfig()

	if err := applyRuntimeOverrides(cfg, "deepseek", "deepseek-v4-pro"); err != nil {
		t.Fatalf("applyRuntimeOverrides() error = %v", err)
	}

	if cfg.API.ActiveProvider != "deepseek" || cfg.Model.Provider != "deepseek" {
		t.Fatalf("provider not applied: api=%q model.provider=%q", cfg.API.ActiveProvider, cfg.Model.Provider)
	}
	if cfg.Model.Name != "deepseek-v4-pro" {
		t.Fatalf("model name = %q, want explicit model", cfg.Model.Name)
	}
}

func TestApplyRuntimeOverrides_ModelOnlyDetectsProvider(t *testing.T) {
	cfg := config.DefaultConfig()

	if err := applyRuntimeOverrides(cfg, "", "MiniMax-M2.7"); err != nil {
		t.Fatalf("applyRuntimeOverrides() error = %v", err)
	}

	if cfg.API.ActiveProvider != "minimax" || cfg.API.Backend != "minimax" || cfg.Model.Provider != "minimax" {
		t.Fatalf("provider not detected from model: api=%q backend=%q model.provider=%q", cfg.API.ActiveProvider, cfg.API.Backend, cfg.Model.Provider)
	}
	if cfg.Model.Name != "MiniMax-M2.7" {
		t.Fatalf("model name = %q, want MiniMax-M2.7", cfg.Model.Name)
	}
}

func TestApplyRuntimeOverrides_UnknownProviderErrors(t *testing.T) {
	cfg := config.DefaultConfig()

	err := applyRuntimeOverrides(cfg, "nope", "")
	if err == nil {
		t.Fatal("applyRuntimeOverrides() error = nil, want unknown provider error")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("error = %v, want unknown provider", err)
	}
}

func TestEvalGateOptions_ParsesThresholds(t *testing.T) {
	opts, enabled, err := evalGateOptions("90%", "2%", true, []string{"verification_passed=100%"})
	if err != nil {
		t.Fatalf("evalGateOptions() error = %v", err)
	}
	if !enabled {
		t.Fatal("evalGateOptions() enabled = false, want true")
	}
	if opts.MinScoreRatio != 0.9 || opts.MaxRegression != 0.02 {
		t.Fatalf("ratios = %v/%v, want 0.9/0.02", opts.MinScoreRatio, opts.MaxRegression)
	}
	if !opts.RequireAllPassed {
		t.Fatal("RequireAllPassed = false, want true")
	}
	if opts.MetricMinRatios["verification_passed"] != 1 {
		t.Fatalf("metric threshold = %v, want 1", opts.MetricMinRatios["verification_passed"])
	}
}

func TestEvalGateOptions_RejectsInvalidMetricThreshold(t *testing.T) {
	_, _, err := evalGateOptions("", "", false, []string{"verification_passed"})
	if err == nil {
		t.Fatal("evalGateOptions() error = nil, want invalid metric threshold error")
	}
	if !strings.Contains(err.Error(), "--fail-metric") {
		t.Fatalf("error = %v, want --fail-metric context", err)
	}
}

func TestApplyAddDirFlags(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.AllowedDirs = nil

	work := t.TempDir()

	// A valid directory is appended in-memory.
	if err := applyAddDirFlags(cfg, []string{work}); err != nil {
		t.Fatalf("valid dir should be accepted: %v", err)
	}
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Fatalf("expected 1 allowed dir, got %v", cfg.Tools.AllowedDirs)
	}

	// Duplicate is deduped (AddAllowedDir).
	if err := applyAddDirFlags(cfg, []string{work}); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tools.AllowedDirs) != 1 {
		t.Errorf("duplicate should be deduped, got %v", cfg.Tools.AllowedDirs)
	}

	// An ungrantable location is refused (and nothing is appended).
	before := len(cfg.Tools.AllowedDirs)
	if err := applyAddDirFlags(cfg, []string{"/etc"}); err == nil {
		t.Error("/etc must be refused")
	}
	if len(cfg.Tools.AllowedDirs) != before {
		t.Error("refused dir must not be appended")
	}

	// A non-existent path errors.
	if err := applyAddDirFlags(cfg, []string{work + "/does-not-exist"}); err == nil {
		t.Error("non-existent path should error")
	}

	// Empty entries are skipped without error.
	if err := applyAddDirFlags(cfg, []string{"", "   "}); err != nil {
		t.Errorf("empty entries should be skipped: %v", err)
	}
}
