package main

import (
	"gokin/internal/config"
	"testing"
)

func TestRuntimeHaikuSelectionKeepsActiveProviderAndOverridesPreset(t *testing.T) {
	for _, provider := range []string{"glm", "ollama"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
			t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", config.GLMFlashModel)
			t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
			cfg := config.DefaultConfig()
			cfg.API.ActiveProvider = provider
			cfg.API.Backend = provider
			cfg.Model.Provider = provider
			cfg.Model.Preset = "coding"
			if err := applyRuntimeOverrides(cfg, "", "haiku"); err != nil {
				t.Fatal(err)
			}
			if cfg.Model.Name != config.GLMFlashModel || cfg.Model.Provider != provider || cfg.API.ActiveProvider != provider || cfg.Model.Preset != "" {
				t.Fatalf("alias changed transport or retained overriding preset: %+v", cfg.Model)
			}
		})
	}
}

func TestRuntimeHaikuProviderSwitchUsesNewProviderDefault(t *testing.T) {
	t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
	t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", "")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "deepseek"
	cfg.Model.Name = "deepseek-v4-pro"
	if err := applyRuntimeOverrides(cfg, "glm", "haiku"); err != nil || cfg.Model.Name != config.GLMFlashModel {
		t.Fatalf("new provider inherited old provider's model: %+v %v", cfg.Model, err)
	}
}
