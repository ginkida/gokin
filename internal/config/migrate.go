package config

import (
	"fmt"
	"strings"

	"gokin/internal/logging"
)

// MigrateConfig migrates old configuration format to new format.
func MigrateConfig(cfg *Config) {
	migrated := false

	// Auto-detect provider if not set
	if cfg.Model.Provider == "" || cfg.Model.Provider == "auto" {
		if cfg.Model.Preset != "" {
			cfg.Model.ApplyPreset(cfg.Model.Preset)
		} else {
			cfg.Model.Provider = DetectProvider(cfg.Model.Name)
		}
		migrated = true
	}

	// Apply preset if specified
	if cfg.Model.Preset != "" {
		if cfg.Model.ApplyPreset(cfg.Model.Preset) {
			migrated = true
		}
	}

	// Normalize backend to match provider
	if cfg.API.Backend == "auto" && cfg.Model.Provider != "" {
		cfg.API.Backend = cfg.Model.Provider
	}

	if migrated {
		// MUST NOT print to stdout: MigrateConfig runs inside client.NewClient
		// on EVERY ApplyConfig (and at boot), and a raw write corrupts the
		// Bubble Tea alt-screen TUI. Log it instead.
		logging.Info("configuration migrated to new format")
	}
}

// DetectProvider determines the provider from model name.
func DetectProvider(modelName string) string {
	return DetectProviderFromModel(modelName)
}

// NormalizeConfig ensures configuration is consistent and valid.
func NormalizeConfig(cfg *Config) error {
	if err := ValidateRetryConfig(cfg); err != nil {
		return err
	}

	// Ensure provider is set
	if cfg.Model.Provider == "" {
		cfg.Model.Provider = DetectProvider(cfg.Model.Name)
	}

	// Ensure backend is set (legacy field, for compatibility)
	if cfg.API.Backend == "" || cfg.API.Backend == "auto" {
		cfg.API.Backend = cfg.Model.Provider
	}

	// Sync ActiveProvider with Backend if not set
	if cfg.API.ActiveProvider == "" {
		cfg.API.ActiveProvider = cfg.API.Backend
	}

	// Validate that we have an API key for the active provider
	// Keys are loaded from environment or config when client is created
	// So we don't validate here - let the client creation handle missing keys

	// Apply preset if specified
	if cfg.Model.Preset != "" {
		if !cfg.Model.ApplyPreset(cfg.Model.Preset) {
			return fmt.Errorf("unknown preset: %s (available: %s)",
				cfg.Model.Preset,
				strings.Join(ListPresets(), ", "))
		}
	} else if matchProvider := providerDefaultPreset(cfg.Model.Provider); matchProvider != "" &&
		looksLikeDefaultModelConfig(&cfg.Model) {
		// Provider was set explicitly (e.g. "glm") but Name/MaxOutputTokens
		// are still the zero-preset defaults (glm-5 / glm-5.1 / glm-5.2 /
		// kimi-for-coding / 131072 — see looksLikeDefaultModelConfig in
		// presets.go). The user almost certainly wanted the provider's
		// preset — apply it so output isn't truncated just because they
		// skipped the preset.
		_ = cfg.Model.ApplyPreset(matchProvider)
	}

	// Normalize retry provider keys for case-insensitive lookups.
	if len(cfg.API.Retry.Providers) > 0 {
		normalized := make(map[string]ProviderRetryConfig, len(cfg.API.Retry.Providers))
		for provider, override := range cfg.API.Retry.Providers {
			key := strings.ToLower(strings.TrimSpace(provider))
			if key == "" {
				continue
			}
			normalized[key] = override
		}
		cfg.API.Retry.Providers = normalized
	}

	// Fallback health labels and factory routing must refer to real providers.
	// A typo previously fell into generic model auto-detection and silently
	// created another GLM client under a bogus provider name.
	if len(cfg.Model.FallbackProviders) > 0 {
		normalized := make([]string, 0, len(cfg.Model.FallbackProviders))
		seen := make(map[string]struct{}, len(cfg.Model.FallbackProviders))
		for _, provider := range cfg.Model.FallbackProviders {
			provider = strings.ToLower(strings.TrimSpace(provider))
			if provider == "" {
				continue
			}
			if GetProvider(provider) == nil {
				return fmt.Errorf("unknown fallback provider %q", provider)
			}
			if _, exists := seen[provider]; exists {
				continue
			}
			seen[provider] = struct{}{}
			normalized = append(normalized, provider)
		}
		cfg.Model.FallbackProviders = normalized
	}

	return nil
}

// GetEffectiveAPIKey returns the effective API key for the active provider.
func (c *Config) GetEffectiveAPIKey() (string, error) {
	key := c.API.GetActiveKey()
	if key != "" {
		return key, nil
	}
	return "", fmt.Errorf("API key not configured for provider: %s", c.API.GetActiveProvider())
}
