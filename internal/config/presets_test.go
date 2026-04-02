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
		{"coding", true, "glm-5", "glm"},
		{"fast", true, "gemini-3-flash-preview", "gemini"},
		{"balanced", true, "gemini-3-flash-preview", "gemini"},
		{"creative", true, "gemini-3-pro-preview", "gemini"},
		{"anthropic", true, "claude-sonnet-4-5-20250929", "anthropic"},
		{"openai", true, "gpt-5.4", "openai"},
		{"ollama", true, "llama3.2", "ollama"},
		{"kimi", true, "kimi-k2.5", "kimi"},
		{"deepseek", true, "deepseek-chat", "deepseek"},
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
	// Verify specific temperature values
	cases := map[string]float32{
		"coding":  0.7,
		"fast":    1.0,
		"kimi":    0.6,
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
		"coding":    131072,
		"fast":      8192,
		"anthropic": 16384,
		"kimi":      32768,
		"ollama":    4096,
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
	if len(presets) != 14 {
		t.Errorf("len(ListPresets) = %d, want 14", len(presets))
	}

	// All returned presets should be valid
	for _, p := range presets {
		if !IsValidPreset(p) {
			t.Errorf("ListPresets returned invalid preset %q", p)
		}
	}
}

func TestPresetCount(t *testing.T) {
	if len(ModelPresets) != 14 {
		t.Errorf("len(ModelPresets) = %d, want 14", len(ModelPresets))
	}
}
