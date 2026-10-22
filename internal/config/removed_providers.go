package config

import (
	"log/slog"
	"strings"

	"gopkg.in/yaml.v3"
)

// RemovedProviderKeys accepts old saved credentials only for migration. These
// keys are discarded and never used to authenticate a supported provider.
type RemovedProviderKeys struct {
	KimiKey    string `yaml:"kimi_key,omitempty"`
	MiniMaxKey string `yaml:"minimax_key,omitempty"`
}

// UnmarshalYAML retains the historical tool-budget setting while giving the
// provider-neutral spelling precedence when both keys are present.
func (c *ToolsConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain ToolsConfig
	if err := node.Decode((*plain)(c)); err != nil {
		return err
	}
	var legacy struct {
		Budget  *int      `yaml:"kimi_tool_budget"`
		Current yaml.Node `yaml:"tool_budget"`
	}
	if err := node.Decode(&legacy); err != nil {
		return err
	}
	if legacy.Current.Kind == 0 && legacy.Budget != nil {
		c.ToolBudget = *legacy.Budget
	}
	return nil
}

func removedProvider(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "kimi", "minimax":
		return true
	}
	return false
}

func removedModel(value, provider string) bool {
	// Ollama model names are local identifiers, including names of hosted models.
	if strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return false
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "kimi") || strings.HasPrefix(value, "minimax") ||
		strings.HasPrefix(value, "moonshot") || value == "k3" || strings.HasPrefix(value, "k3-")
}

func migrateRemovedProviders(cfg *Config) {
	if cfg == nil {
		return
	}
	migratePrimary := removedProvider(cfg.API.ActiveProvider) || removedProvider(cfg.API.Backend) ||
		removedProvider(cfg.Model.Provider) || removedProvider(cfg.Model.Preset) || removedModel(cfg.Model.Name, cfg.Model.Provider) ||
		cfg.API.KimiKey != "" || cfg.API.MiniMaxKey != ""
	removed := migratePrimary
	fallbacks := make([]string, 0, len(cfg.Model.FallbackProviders))
	for _, provider := range cfg.Model.FallbackProviders {
		if removedProvider(provider) {
			removed = true
			continue
		}
		fallbacks = append(fallbacks, provider)
	}
	for provider := range cfg.API.Retry.Providers {
		if removedProvider(provider) {
			delete(cfg.API.Retry.Providers, provider)
			removed = true
		}
	}
	if !removed {
		return
	}
	cfg.Model.FallbackProviders = fallbacks
	message := "Kimi and MiniMax providers were removed; discarded their fallback and retry settings"
	if migratePrimary {
		cfg.API.RemovedProviderKeys = RemovedProviderKeys{}
		cfg.API.ActiveProvider, cfg.API.Backend = "glm", "glm"
		// A saved generic key or custom gateway may belong to the removed provider.
		cfg.API.APIKey = ""
		cfg.Model.CustomBaseURL = ""
		cfg.Model.Preset = ""
		cfg.Model.ApplyPreset("glm")
		message = "Kimi and MiniMax providers were removed; configuration migrated to GLM with its default model. Configure a GLM key with /login glm if needed"
	}
	if cfg.removedProviderWarned {
		return
	}
	cfg.removedProviderWarned = true
	slog.Warn(message)
}

// DiscardRemovedProviderSettings prevents an explicit wizard selection from
// being migrated back to GLM when the saved YAML is loaded again. Other settings
// remain in the raw tree so setup preserves unknown and unrelated fields.
func DiscardRemovedProviderSettings(api, model map[string]any) {
	value := func(section map[string]any, key string) string { s, _ := section[key].(string); return s }
	removed := removedProvider(value(api, "active_provider")) || removedProvider(value(api, "backend")) ||
		removedProvider(value(model, "provider")) || removedProvider(value(model, "preset")) || removedModel(value(model, "name"), value(model, "provider")) ||
		value(api, "kimi_key") != "" || value(api, "minimax_key") != ""
	if fallbacks, ok := model["fallback_providers"].([]any); ok {
		retained := make([]any, 0, len(fallbacks))
		for _, fallback := range fallbacks {
			provider, _ := fallback.(string)
			if !removedProvider(provider) {
				retained = append(retained, fallback)
			}
		}
		model["fallback_providers"] = retained
	}
	if retry, ok := api["retry"].(map[string]any); ok {
		if providers, ok := retry["providers"].(map[string]any); ok {
			for provider := range providers {
				if removedProvider(provider) {
					delete(providers, provider)
				}
			}
		}
	}
	delete(api, "kimi_key")
	delete(api, "minimax_key")
	if removed {
		delete(api, "api_key")
		delete(model, "custom_base_url")
		delete(model, "preset")
	}
}
