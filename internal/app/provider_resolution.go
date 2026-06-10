package app

import (
	"strings"

	"gokin/internal/config"
)

// runtimeProviderForConfig returns the provider that should drive runtime
// behavior such as prompt addenda, routing heuristics, and retry policy.
func runtimeProviderForConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if provider := normalizeProvider(cfg.Model.Provider); provider != "" && provider != "auto" {
		return provider
	}
	if model := strings.TrimSpace(cfg.Model.Name); model != "" {
		if detected := normalizeProvider(config.DetectKnownProviderFromModel(model)); detected != "" {
			return detected
		}
	}
	return normalizeProvider(cfg.API.GetActiveProvider())
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
