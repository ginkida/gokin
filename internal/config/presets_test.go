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
		{"coding", true, "kimi-for-coding", "kimi"},
		{"fast", true, "kimi-for-coding", "kimi"},
		{"balanced", true, "kimi-for-coding", "kimi"},
		{"creative", true, "MiniMax-M2.7", "minimax"},
		{"ollama", true, "llama3.2", "ollama"},
		{"kimi", true, "kimi-for-coding", "kimi"},
		{"glm", true, "glm-5", "glm"},
		{"minimax", true, "MiniMax-M2.7", "minimax"},
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
		"coding":  0.6, // kimi-for-coding default
		"kimi":    0.6,
		"glm":     0.7,
		"ollama":  0.7,
		"minimax": 0.7,
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
		"coding": 32768, // kimi-for-coding output cap
		"fast":   32768,
		"kimi":   32768,
		"glm":    131072,
		"ollama": 4096,
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
	// 8 presets: coding, fast, balanced, creative, glm, kimi, minimax, ollama
	if len(presets) != 8 {
		t.Errorf("len(ListPresets) = %d, want 8", len(presets))
	}
	for _, p := range presets {
		if !IsValidPreset(p) {
			t.Errorf("ListPresets returned invalid preset %q", p)
		}
	}
}

func TestPresetCount(t *testing.T) {
	if len(ModelPresets) != 8 {
		t.Errorf("len(ModelPresets) = %d, want 8", len(ModelPresets))
	}
}

func TestProviderDefaultPreset(t *testing.T) {
	cases := map[string]string{
		"glm":     "glm",
		"kimi":    "kimi",
		"minimax": "minimax",
		"ollama":  "ollama",
		"unknown": "", // unrecognised providers must not auto-apply
		"":        "",
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
		{"glm default name, zero tokens", &ModelConfig{Name: "glm-5"}, true},
		{"kimi-for-coding default, zero tokens", &ModelConfig{Name: "kimi-for-coding"}, true},
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

	// Explicit minimax preset wins over auto-apply heuristic
	cfg = &Config{Model: ModelConfig{
		Provider: "glm",
		Preset:   "minimax",
	}}
	if err := NormalizeConfig(cfg); err != nil {
		t.Fatalf("NormalizeConfig: %v", err)
	}
	if cfg.Model.Name != "MiniMax-M2.7" {
		t.Errorf("Name = %q, want MiniMax-M2.7 (explicit preset)", cfg.Model.Name)
	}
}
