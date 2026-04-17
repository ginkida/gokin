package config

import (
	"testing"
)

func TestHasOAuthToken(t *testing.T) {
	tests := []struct {
		name     string
		api      APIConfig
		provider string
		want     bool
	}{
		{
			name:     "gemini with valid OAuth",
			api:      APIConfig{GeminiOAuth: &OAuthTokenConfig{RefreshToken: "token123"}},
			provider: "gemini",
			want:     true,
		},
		{
			name:     "gemini with nil OAuth",
			api:      APIConfig{},
			provider: "gemini",
			want:     false,
		},
		{
			name:     "gemini with empty refresh token",
			api:      APIConfig{GeminiOAuth: &OAuthTokenConfig{RefreshToken: ""}},
			provider: "gemini",
			want:     false,
		},
		{
			name:     "unknown provider",
			api:      APIConfig{},
			provider: "unknown",
			want:     false,
		},
		{
			name:     "anthropic has no OAuth",
			api:      APIConfig{},
			provider: "anthropic",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.api.HasOAuthToken(tt.provider)
			if got != tt.want {
				t.Errorf("HasOAuthToken(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestGetActiveProvider(t *testing.T) {
	tests := []struct {
		name string
		api  APIConfig
		want string
	}{
		{
			name: "ActiveProvider set",
			api:  APIConfig{ActiveProvider: "anthropic"},
			want: "anthropic",
		},
		{
			name: "fallback to Backend",
			api:  APIConfig{Backend: "glm"},
			want: "glm",
		},
		{
			name: "default to gemini",
			api:  APIConfig{},
			want: "gemini",
		},
		{
			name: "ActiveProvider takes precedence over Backend",
			api:  APIConfig{ActiveProvider: "deepseek", Backend: "glm"},
			want: "deepseek",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.api.GetActiveProvider()
			if got != tt.want {
				t.Errorf("GetActiveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHasProvider(t *testing.T) {
	tests := []struct {
		name     string
		api      APIConfig
		provider string
		want     bool
	}{
		{
			name:     "ollama always true (KeyOptional, no OAuth)",
			api:      APIConfig{},
			provider: "ollama",
			want:     true,
		},
		{
			name:     "anthropic with key",
			api:      APIConfig{AnthropicKey: "sk-ant-123"},
			provider: "anthropic",
			want:     true,
		},
		{
			name:     "anthropic without key",
			api:      APIConfig{},
			provider: "anthropic",
			want:     false,
		},
		{
			name:     "gemini with key",
			api:      APIConfig{GeminiKey: "AIza123"},
			provider: "gemini",
			want:     true,
		},
		{
			name:     "gemini with OAuth only",
			api:      APIConfig{GeminiOAuth: &OAuthTokenConfig{RefreshToken: "tok"}},
			provider: "gemini",
			want:     true,
		},
		{
			name:     "gemini with legacy key and active provider match",
			api:      APIConfig{APIKey: "legacy-key", ActiveProvider: "gemini"},
			provider: "gemini",
			want:     true,
		},
		{
			name:     "gemini with legacy key but different active provider",
			api:      APIConfig{APIKey: "legacy-key", ActiveProvider: "anthropic"},
			provider: "gemini",
			want:     false,
		},
		{
			name:     "unknown provider",
			api:      APIConfig{},
			provider: "nonexistent",
			want:     false,
		},
		{
			name:     "glm with key",
			api:      APIConfig{GLMKey: "glm-key"},
			provider: "glm",
			want:     true,
		},
		{
			name:     "deepseek with key",
			api:      APIConfig{DeepSeekKey: "ds-key"},
			provider: "deepseek",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.api.HasProvider(tt.provider)
			if got != tt.want {
				t.Errorf("HasProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestGetActiveKey(t *testing.T) {
	tests := []struct {
		name string
		api  APIConfig
		want string
	}{
		{
			name: "provider key found",
			api:  APIConfig{ActiveProvider: "anthropic", AnthropicKey: "sk-123"},
			want: "sk-123",
		},
		{
			name: "fallback to legacy key",
			api:  APIConfig{ActiveProvider: "gemini", APIKey: "legacy"},
			want: "legacy",
		},
		{
			name: "unknown provider falls back to legacy",
			api:  APIConfig{ActiveProvider: "unknown", APIKey: "fallback"},
			want: "fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.api.GetActiveKey()
			if got != tt.want {
				t.Errorf("GetActiveKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetProviderKey(t *testing.T) {
	api := APIConfig{}

	api.SetProviderKey("anthropic", "sk-new")
	if api.AnthropicKey != "sk-new" {
		t.Errorf("SetProviderKey(anthropic) failed: got %q", api.AnthropicKey)
	}

	api.SetProviderKey("gemini", "AIza-new")
	if api.GeminiKey != "AIza-new" {
		t.Errorf("SetProviderKey(gemini) failed: got %q", api.GeminiKey)
	}

	// Unknown provider should not panic
	api.SetProviderKey("nonexistent", "key")
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Model.Name != "gemini-3-flash-preview" {
		t.Errorf("Model.Name = %q, want gemini-3-flash-preview", cfg.Model.Name)
	}
	if cfg.Model.Temperature != 1.0 {
		t.Errorf("Model.Temperature = %v, want 1.0", cfg.Model.Temperature)
	}
	if cfg.Model.MaxOutputTokens != 8192 {
		t.Errorf("Model.MaxOutputTokens = %v, want 8192", cfg.Model.MaxOutputTokens)
	}
	if cfg.Tools.Bash.Sandbox != true {
		t.Error("Tools.Bash.Sandbox should be true by default")
	}
	if cfg.Context.WarningThreshold != 0.8 {
		t.Errorf("Context.WarningThreshold = %v, want 0.8", cfg.Context.WarningThreshold)
	}
	if cfg.DoneGate.Mode != "strict" {
		t.Errorf("DoneGate.Mode = %q, want strict", cfg.DoneGate.Mode)
	}
	if cfg.Plan.Algorithm != "beam" {
		t.Errorf("Plan.Algorithm = %q, want beam", cfg.Plan.Algorithm)
	}
	if !cfg.Permission.Enabled {
		t.Error("Permission.Enabled should be true by default")
	}
	if !cfg.RateLimit.Enabled {
		t.Error("RateLimit.Enabled should be true by default")
	}
	if cfg.RateLimit.RequestsPerMinute != 60 {
		t.Errorf("RateLimit.RequestsPerMinute = %d, want 60", cfg.RateLimit.RequestsPerMinute)
	}
}
