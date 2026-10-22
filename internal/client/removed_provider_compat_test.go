package client

import (
	"strings"
	"testing"

	"gokin/internal/config"
)

func TestNewClientRemovedModelMigrationUsesDefaultModel(t *testing.T) {
	for _, model := range []string{"k3", "kimi-k2.6", "MiniMax-M2.7"} {
		t.Run(model, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.API.GLMKey = "test-glm-key-for-removed-provider-migration"
			cfg.Model.Name = model
			c, err := NewClient(t.Context(), cfg, cfg.Model.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer ClosePool()
			if c.GetModel() != config.DefaultGLMModel || cfg.Model.Name != config.DefaultGLMModel {
				t.Fatalf("client/config models = %q/%q, want GLM default %q", c.GetModel(), cfg.Model.Name, config.DefaultGLMModel)
			}
		})
	}
}

func TestNewClientRemovedModelOverrideRejected(t *testing.T) {
	for _, model := range []string{"k3", "kimi-k2.6", "MiniMax-M2.7"} {
		t.Run(model, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.API.GLMKey = "test-glm-key-for-removed-provider-migration"
			c, err := NewClient(t.Context(), cfg, model)
			defer ClosePool()
			if err == nil {
				t.Fatalf("removed model override built client with model %q", c.GetModel())
			}
			if !strings.Contains(err.Error(), "removed provider") || !strings.Contains(err.Error(), "glm, deepseek, ollama") {
				t.Fatalf("error lacks removed-provider guidance: %v", err)
			}
		})
	}
}

func TestOllamaRemovedHostedModelFamiliesKeepNativeTools(t *testing.T) {
	for _, model := range []string{"kimi-k2:latest", "Kimi-K2.6:latest", "kimi-for-coding:latest", "k3:latest", "moonshot:latest", "MiniMax-M2.7:latest"} {
		t.Run(model, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.API.ActiveProvider, cfg.API.Backend = "ollama", "ollama"
			cfg.Model.Provider, cfg.Model.Name = "ollama", model
			c, err := NewClient(t.Context(), cfg, cfg.Model.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer ClosePool()
			ollama, ok := c.(*OllamaClient)
			if !ok || c.GetModel() != model {
				t.Fatalf("local model was migrated: client=%T model=%q", c, c.GetModel())
			}
			if ollama.NeedsToolCallFallback() {
				t.Errorf("Ollama model %q lost native tool support", model)
			}
		})
	}
}
