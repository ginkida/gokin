package config

import (
	"testing"
)

func TestGetProvider(t *testing.T) {
	// All 5 providers must be findable
	for _, name := range []string{"glm", "minimax", "kimi", "deepseek", "ollama"} {
		p := GetProvider(name)
		if p == nil {
			t.Errorf("GetProvider(%q) = nil, want non-nil", name)
			continue
		}
		if p.Name != name {
			t.Errorf("GetProvider(%q).Name = %q", name, p.Name)
		}
	}

	// Unknown provider
	if p := GetProvider("nonexistent"); p != nil {
		t.Errorf("GetProvider(nonexistent) = %v, want nil", p)
	}
}

func TestProviderCount(t *testing.T) {
	if len(Providers) != 5 {
		t.Errorf("len(Providers) = %d, want 5", len(Providers))
	}
}

func TestProviderNames(t *testing.T) {
	names := ProviderNames()
	if len(names) != 5 {
		t.Errorf("len(ProviderNames) = %d, want 5", len(names))
	}
	if names[len(names)-1] != "ollama" {
		t.Errorf("last provider = %q, want ollama", names[len(names)-1])
	}
}

func TestKeyProviderNames(t *testing.T) {
	names := KeyProviderNames()
	// Should exclude ollama (KeyOptional)
	for _, name := range names {
		if name == "ollama" {
			t.Errorf("KeyProviderNames should not contain %q", name)
		}
	}
	// 4 key-required providers: glm, minimax, kimi, deepseek.
	if len(names) != 4 {
		t.Errorf("len(KeyProviderNames) = %d, want 4", len(names))
	}
}

func TestAllProviderNames(t *testing.T) {
	names := AllProviderNames()
	if names[len(names)-1] != "all" {
		t.Errorf("AllProviderNames last = %q, want all", names[len(names)-1])
	}
	if len(names) != 6 { // 5 providers + "all"
		t.Errorf("len(AllProviderNames) = %d, want 6", len(names))
	}
}

func TestDetectProviderFromModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		// GLM
		{"glm-5", "glm"},
		{"glm-4.7", "glm"},
		// MiniMax
		{"minimax-m2.5", "minimax"},
		// Kimi
		{"kimi-k2.5", "kimi"},
		{"moonshot-v1", "kimi"}, // moonshot prefix -> kimi
		// DeepSeek
		{"deepseek-v4-pro", "deepseek"},
		{"deepseek-v4-flash", "deepseek"},
		{"deepseek-chat", "deepseek"},
		{"deepseek-reasoner", "deepseek"},
		// Ollama local models
		{"llama3.2", "ollama"},
		{"qwen2.5-coder", "ollama"},
		{"codellama", "ollama"},
		{"mistral", "ollama"},
		{"phi4", "ollama"},
		// Empty -> default glm
		{"", "glm"},
		// Unknown -> default glm
		{"some-unknown-model", "glm"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := DetectProviderFromModel(tt.model)
			if got != tt.want {
				t.Errorf("DetectProviderFromModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestAnyProviderHasKey(t *testing.T) {
	tests := []struct {
		name string
		api  APIConfig
		want bool
	}{
		{
			name: "no keys",
			api:  APIConfig{},
			want: false,
		},
		{
			name: "glm key",
			api:  APIConfig{GLMKey: "key"},
			want: true,
		},
		{
			name: "legacy API key",
			api:  APIConfig{APIKey: "legacy"},
			want: true,
		},
		{
			name: "kimi key",
			api:  APIConfig{KimiKey: "kimi"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AnyProviderHasKey(&tt.api)
			if got != tt.want {
				t.Errorf("AnyProviderHasKey() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestProviderDefaultModels(t *testing.T) {
	expected := map[string]string{
		"glm":     "glm-5.1",
		"minimax": "MiniMax-M2.7",
		"kimi":    "kimi-for-coding",
		"ollama":  "llama3.2",
	}

	for name, wantModel := range expected {
		p := GetProvider(name)
		if p == nil {
			t.Errorf("GetProvider(%q) = nil", name)
			continue
		}
		if p.DefaultModel != wantModel {
			t.Errorf("provider %q DefaultModel = %q, want %q", name, p.DefaultModel, wantModel)
		}
	}
}

func TestProviderFlags(t *testing.T) {
	// Ollama: KeyOptional, no HasOAuth
	oll := GetProvider("ollama")
	if !oll.KeyOptional {
		t.Error("ollama should be KeyOptional")
	}
	if oll.HasOAuth {
		t.Error("ollama should not have HasOAuth")
	}

	// GLM: not KeyOptional, no OAuth, UsesLegacyKey
	gm := GetProvider("glm")
	if gm.KeyOptional {
		t.Error("glm should not be KeyOptional")
	}
	if gm.HasOAuth {
		t.Error("glm should not have HasOAuth")
	}
	if !gm.UsesLegacyKey {
		t.Error("glm should use legacy key")
	}
}
