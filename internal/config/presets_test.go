package config

import (
	"testing"
)

func TestApplyPreset(t *testing.T) {
	tests := []struct {
		preset       string
		wantOK       bool
		wantModel    string
		wantProvider string
	}{
		{"coding", true, "glm-5.3-flash", "glm"},
		{"fast", true, "glm-5.3-flash", "glm"},
		{"balanced", true, "glm-5.3", "glm"},
		{"creative", true, DefaultGLMModel, "glm"},
		{"deepseek", true, "deepseek-v4-pro", "deepseek"},

		{"ollama", true, "llama3.2", "ollama"},

		{"glm", true, "glm-5.3-flash", "glm"},

		{"nonexistent", false, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.preset, func(t *testing.T) {
			m := &ModelConfig{}
			ok := m.ApplyPreset(tt.preset)
			if ok != tt.wantOK {
				t.Errorf("ApplyPreset(%q) = %v, want %v", tt.preset, ok, tt.wantOK)
			}
			if ok {
				if m.Name != tt.wantModel {
					t.Errorf("after preset %q: Name = %q, want %q", tt.preset, m.Name, tt.wantModel)
				}
				if m.Provider != tt.wantProvider {
					t.Errorf("after preset %q: Provider = %q, want %q", tt.preset, m.Provider, tt.wantProvider)
				}
			}
		})
	}
}

func TestApplyPresetTemperature(t *testing.T) {
	cases := map[string]float32{
		"coding":   1.0, // glm-5.3 provider recommendation
		"deepseek": 0.6,
		"glm":      1.0,
		"ollama":   0.7,
	}
	for preset, wantTemp := range cases {
		m := &ModelConfig{}
		m.ApplyPreset(preset)
		if m.Temperature != wantTemp {
			t.Errorf("preset %q: Temperature = %v, want %v", preset, m.Temperature, wantTemp)
		}
	}
}

func TestApplyPresetMaxTokens(t *testing.T) {
	cases := map[string]int32{
		"coding":   65536, // glm-5.3 default coding cap (headroom)
		"fast":     65536,
		"deepseek": 16384,
		"glm":      131072,
		"ollama":   4096,
	}
	for preset, wantTokens := range cases {
		m := &ModelConfig{}
		m.ApplyPreset(preset)
		if m.MaxOutputTokens != wantTokens {
			t.Errorf("preset %q: MaxOutputTokens = %d, want %d", preset, m.MaxOutputTokens, wantTokens)
		}
	}
}

func TestIsValidPreset(t *testing.T) {
	if !IsValidPreset("coding") {
		t.Error("IsValidPreset(coding) should be true")
	}
	if !IsValidPreset("fast") {
		t.Error("IsValidPreset(fast) should be true")
	}
	if IsValidPreset("nonexistent") {
		t.Error("IsValidPreset(nonexistent) should be false")
	}
	if IsValidPreset("") {
		t.Error("IsValidPreset('') should be false")
	}
}

func TestListPresets(t *testing.T) {
	presets := ListPresets()
	// Seven presets: coding, fast, balanced, creative, glm, deepseek, ollama.
	if len(presets) != 7 {
		t.Errorf("len(ListPresets) = %d, want 7", len(presets))
	}
	for _, p := range presets {
		if !IsValidPreset(p) {
			t.Errorf("ListPresets returned invalid preset %q", p)
		}
	}
}

func TestPresetCount(t *testing.T) {
	if len(ModelPresets) != 7 {
		t.Errorf("len(ModelPresets) = %d, want 7", len(ModelPresets))
	}
}

func TestProviderDefaultPreset(t *testing.T) {
	cases := map[string]string{
		"glm":      "glm",
		"deepseek": "deepseek",
		"ollama":   "ollama",
		"unknown":  "", // unrecognised providers must not auto-apply
		"":         "",
	}
	for provider, want := range cases {
		if got := providerDefaultPreset(provider); got != want {
			t.Errorf("providerDefaultPreset(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestLooksLikeDefaultModelConfig(t *testing.T) {
	cases := []struct {
		name string
		m    *ModelConfig
		want bool
	}{
		{"nil", nil, false},
		{"empty name, zero tokens", &ModelConfig{}, true},
		{"empty name, default tokens", &ModelConfig{MaxOutputTokens: 131072}, true},
		{"glm default name, default tokens", &ModelConfig{Name: "glm-5", MaxOutputTokens: 131072}, true},
		{"glm 5.1 default name, default tokens", &ModelConfig{Name: "glm-5.1", MaxOutputTokens: 131072}, true},
		{"glm 5.2 default name, default tokens", &ModelConfig{Name: "glm-5.2", MaxOutputTokens: 131072}, true},
		{"glm default name, zero tokens", &ModelConfig{Name: "glm-5"}, true},

		{"custom name blocks auto-apply", &ModelConfig{Name: "glm-4.5", MaxOutputTokens: 131072}, false},
		{"custom tokens blocks auto-apply", &ModelConfig{Name: "glm-5", MaxOutputTokens: 16384}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := looksLikeDefaultModelConfig(tc.m); got != tc.want {
				t.Errorf("looksLikeDefaultModelConfig = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeConfig_AutoApplyProviderPreset(t *testing.T) {
	// User customised Name → leave alone (don't override intent)
	cfg := &Config{Model: ModelConfig{
		Provider:        "glm",
		Name:            "glm-4.5",
		MaxOutputTokens: 8192,
	}}
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Model.Name != "glm-4.5" {
		t.Errorf("Name = %q, want glm-4.5 preserved", cfg.Model.Name)
	}

	// Explicit glm preset wins over auto-apply heuristic
	cfg = &Config{Model: ModelConfig{
		Provider: "glm",
		Preset:   "glm",
	}}
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Model.Name != DefaultGLMModel {
		t.Errorf("Name = %q, want deepseek-v4-pro (explicit preset)", cfg.Model.Name)
	}
}
