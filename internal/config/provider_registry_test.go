package config

import (
	"testing"
)

func TestGetProvider(t *testing.T) {
	// All 8 providers must be findable
	for _, name := range []string{"gemini", "anthropic", "glm", "deepseek", "minimax", "kimi", "openai", "ollama"} {
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
	if len(Providers) != 8 {
		t.Errorf("len(Providers) = %d, want 8", len(Providers))
	}
}

func TestProviderNames(t *testing.T) {
	names := ProviderNames()
	if len(names) != 8 {
		t.Errorf("len(ProviderNames) = %d, want 8", len(names))
	}
	// First should be gemini, last should be ollama (ordered)
	if names[0] != "gemini" {
		t.Errorf("first provider = %q, want gemini", names[0])
	}
	if names[len(names)-1] != "ollama" {
		t.Errorf("last provider = %q, want ollama", names[len(names)-1])
	}
}

func TestKeyProviderNames(t *testing.T) {
	names := KeyProviderNames()
	// Should exclude ollama and openai (both KeyOptional)
	for _, name := range names {
		if name == "ollama" || name == "openai" {
			t.Errorf("KeyProviderNames should not contain %q", name)
		}
	}
	if len(names) != 6 {
		t.Errorf("len(KeyProviderNames) = %d, want 6", len(names))
	}
}

func TestAllProviderNames(t *testing.T) {
	names := AllProviderNames()
	if names[len(names)-1] != "all" {
		t.Errorf("AllProviderNames last = %q, want all", names[len(names)-1])
	}
	if len(names) != 9 { // 8 providers + "all"
		t.Errorf("len(AllProviderNames) = %d, want 9", len(names))
	}
}

func TestDetectProviderFromModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		// Gemini models
		{"gemini-3-flash-preview", "gemini"},
		{"gemini-2.5-pro", "gemini"},
		{"models/gemini-3-pro", "gemini"}, // models/ prefix
		// Anthropic
		{"claude-sonnet-4-5-20250929", "anthropic"},
		{"claude-opus-4-6", "anthropic"},
		// OpenAI
		{"gpt-5.3-codex", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"o4-mini", "openai"},
		// GLM
		{"glm-5", "glm"},
		{"glm-4.7", "glm"},
		// DeepSeek
		{"deepseek-chat", "deepseek"},
		{"deepseek-reasoner", "deepseek"},
		// MiniMax
		{"minimax-m2.5", "minimax"},
		// Kimi
		{"kimi-k2.5", "kimi"},
		{"moonshot-v1", "kimi"}, // moonshot prefix -> kimi
		// Ollama local models
		{"llama3.2", "ollama"},
		{"qwen2.5-coder", "ollama"},
		{"codellama", "ollama"},
		{"mistral", "ollama"},
		{"phi4", "ollama"},
		// Case insensitive
		{"GEMINI-3-flash", "gemini"},
		{"Claude-Opus-4-6", "anthropic"},
		{"GPT-5.3-codex", "openai"},
		// Empty -> default gemini
		{"", "gemini"},
		// Unknown -> default gemini
		{"some-unknown-model", "gemini"},
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
			name: "gemini key",
			api:  APIConfig{GeminiKey: "key"},
			want: true,
		},
		{
			name: "legacy API key",
			api:  APIConfig{APIKey: "legacy"},
			want: true,
		},
		{
			name: "gemini OAuth",
			api:  APIConfig{GeminiOAuth: &OAuthTokenConfig{RefreshToken: "tok"}},
			want: true,
		},
		{
			name: "openai OAuth",
			api:  APIConfig{OpenAIOAuth: &OAuthTokenConfig{RefreshToken: "tok"}},
			want: true,
		},
		{
			name: "anthropic key",
			api:  APIConfig{AnthropicKey: "sk"},
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
		"gemini":    "gemini-3-flash-preview",
		"anthropic": "claude-sonnet-4-5-20250929",
		"openai":    "gpt-5.4",
		"glm":       "glm-5",
		"deepseek":  "deepseek-chat",
		"minimax":   "MiniMax-M2.5",
		"kimi":      "kimi-k2.5",
		"ollama":    "llama3.2",
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
	// OpenAI: KeyOptional + HasOAuth, no EnvVars
	oai := GetProvider("openai")
	if !oai.KeyOptional {
		t.Error("openai should be KeyOptional")
	}
	if !oai.HasOAuth {
		t.Error("openai should have HasOAuth")
	}
	if len(oai.EnvVars) != 0 {
		t.Errorf("openai EnvVars should be empty, got %v", oai.EnvVars)
	}

	// Ollama: KeyOptional, no HasOAuth
	oll := GetProvider("ollama")
	if !oll.KeyOptional {
		t.Error("ollama should be KeyOptional")
	}
	if oll.HasOAuth {
		t.Error("ollama should not have HasOAuth")
	}

	// Gemini: not KeyOptional, HasOAuth, UsesLegacyKey
	gem := GetProvider("gemini")
	if gem.KeyOptional {
		t.Error("gemini should not be KeyOptional")
	}
	if !gem.HasOAuth {
		t.Error("gemini should have HasOAuth")
	}
	if !gem.UsesLegacyKey {
		t.Error("gemini should use legacy key")
	}
}
