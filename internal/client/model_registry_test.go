package client

import (
	"testing"

	"gokin/internal/config"
)

func TestAvailableModelsIncludeProviderDefaults(t *testing.T) {
	for _, provider := range config.Providers {
		if provider.KeyOptional {
			continue
		}
		if provider.DefaultModel == "" {
			t.Fatalf("provider %s has empty DefaultModel", provider.Name)
		}
		info, ok := GetModelInfo(provider.DefaultModel)
		if !ok {
			t.Fatalf("provider %s default model %q is not in client.AvailableModels", provider.Name, provider.DefaultModel)
		}
		if info.Provider != provider.Name {
			t.Fatalf("provider %s default model %q is registered under provider %q", provider.Name, provider.DefaultModel, info.Provider)
		}
	}
}

func TestAvailableModelsIncludePresets(t *testing.T) {
	for preset, model := range config.ModelPresets {
		if model.Provider == "ollama" {
			continue
		}
		info, ok := GetModelInfo(model.Name)
		if !ok {
			t.Fatalf("preset %s model %q is not in client.AvailableModels", preset, model.Name)
		}
		if info.Provider != model.Provider {
			t.Fatalf("preset %s model %q is registered under provider %q, want %q",
				preset, model.Name, info.Provider, model.Provider)
		}
	}
}
