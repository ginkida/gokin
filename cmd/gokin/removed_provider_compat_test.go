package main

import (
	"strings"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
)

func TestRemovedProviderCLIOverrideRejected(t *testing.T) {
	for _, provider := range []string{"kimi", "minimax"} {
		err := applyRuntimeOverrides(config.DefaultConfig(), provider, "")
		if err == nil || !strings.Contains(err.Error(), "supported: glm, deepseek, ollama") {
			t.Fatalf("provider %q: error=%v", provider, err)
		}
	}
}

func TestRemovedModelCLIOverrideMigratesConsistently(t *testing.T) {
	for _, model := range []string{"k3", "kimi-k2.6", "MiniMax-M2.7"} {
		t.Run(model, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.API.GLMKey = "test-glm-key-for-removed-provider-migration"
			if err := applyRuntimeOverrides(cfg, "", model); err != nil {
				t.Fatal(err)
			}
			c, err := client.NewClient(t.Context(), cfg, cfg.Model.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer client.ClosePool()
			if c.GetModel() != config.DefaultGLMModel || cfg.Model.Provider != "glm" {
				t.Fatalf("CLI override built %s/%s, want glm/%s", cfg.Model.Provider, c.GetModel(), config.DefaultGLMModel)
			}
		})
	}
}
