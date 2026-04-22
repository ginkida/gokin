package config

// ModelPreset defines a model preset configuration.
type ModelPreset struct {
	Provider        string
	Name            string
	Temperature     float32
	MaxOutputTokens int32
}

// ModelPresets contains predefined model configurations. Kimi Coding Plan
// is the default across coding/fast/balanced — gokin's primary target is
// active coding sessions, and kimi-for-coding (K2.6) has the largest
// context window (262K) of any subscription-tier model we ship against.
var ModelPresets = map[string]ModelPreset{
	"coding": {
		Provider:        "kimi",
		Name:            "kimi-for-coding",
		Temperature:     0.6,
		MaxOutputTokens: 32768,
	},
	"fast": {
		Provider:        "kimi",
		Name:            "kimi-for-coding",
		Temperature:     0.6,
		MaxOutputTokens: 32768,
	},
	"balanced": {
		Provider:        "kimi",
		Name:            "kimi-for-coding",
		Temperature:     0.6,
		MaxOutputTokens: 32768,
	},
	"creative": {
		Provider:        "minimax",
		Name:            "MiniMax-M2.7",
		Temperature:     0.7,
		MaxOutputTokens: 16384,
	},
	"glm": {
		Provider:        "glm",
		Name:            "glm-5",
		Temperature:     0.7,
		MaxOutputTokens: 131072,
	},
	"kimi": {
		Provider:        "kimi",
		Name:            "kimi-for-coding",
		Temperature:     0.6,
		MaxOutputTokens: 32768,
	},
	"minimax": {
		Provider:        "minimax",
		Name:            "MiniMax-M2.7",
		Temperature:     0.7,
		MaxOutputTokens: 16384,
	},
	"ollama": {
		Provider:        "ollama",
		Name:            "llama3.2",
		Temperature:     0.7,
		MaxOutputTokens: 4096,
	},
}

// ApplyPreset applies a model preset to the ModelConfig.
// Returns true if preset was applied successfully, false if preset not found.
func (m *ModelConfig) ApplyPreset(preset string) bool {
	p, ok := ModelPresets[preset]
	if !ok {
		return false
	}

	m.Provider = p.Provider
	m.Name = p.Name
	m.Temperature = p.Temperature
	m.MaxOutputTokens = p.MaxOutputTokens
	return true
}

// IsValidPreset checks if a preset name is valid.
func IsValidPreset(preset string) bool {
	_, ok := ModelPresets[preset]
	return ok
}

// ListPresets returns all available preset names.
func ListPresets() []string {
	presets := make([]string, 0, len(ModelPresets))
	for name := range ModelPresets {
		presets = append(presets, name)
	}
	return presets
}

// providerDefaultPreset returns the preset name that matches a provider, or
// empty string if there is no canonical preset. Used by auto-apply logic so a
// user who sets `provider: glm` without an explicit preset still gets the
// provider-appropriate output cap and model name.
func providerDefaultPreset(provider string) string {
	switch provider {
	case "glm", "kimi", "minimax", "ollama":
		return provider
	}
	return ""
}

// looksLikeDefaultModelConfig reports whether a ModelConfig still holds the
// zero-preset defaults (glm-5 / 131072 output). Used to decide whether
// auto-applying a provider preset is safe: if the user customised Name or
// MaxOutputTokens themselves, we leave their values alone.
func looksLikeDefaultModelConfig(m *ModelConfig) bool {
	if m == nil {
		return false
	}
	defaultName := m.Name == "" || m.Name == "glm-5" || m.Name == "kimi-for-coding"
	defaultMax := m.MaxOutputTokens == 0 || m.MaxOutputTokens == 131072
	return defaultName && defaultMax
}
