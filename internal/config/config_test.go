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
			name: "default to glm",
			api:  APIConfig{},
			want: "glm",
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
			name:     "glm with key",
			api:      APIConfig{GLMKey: "glm-123"},
			provider: "glm",
			want:     true,
		},
		{
			name:     "glm with legacy key and active provider match",
			api:      APIConfig{APIKey: "legacy-key", ActiveProvider: "glm"},
			provider: "glm",
			want:     true,
		},
		{
			name:     "glm without key",
			api:      APIConfig{},
			provider: "glm",
			want:     false,
		},
		{
			name:     "unknown provider",
			api:      APIConfig{},
			provider: "nonexistent",
			want:     false,
		},
		{
			name:     "kimi with key",
			api:      APIConfig{KimiKey: "kimi-key"},
			provider: "kimi",
			want:     true,
		},
		{
			name:     "minimax with key",
			api:      APIConfig{MiniMaxKey: "mm-key"},
			provider: "minimax",
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
			api:  APIConfig{ActiveProvider: "glm", GLMKey: "glm-123"},
			want: "glm-123",
		},
		{
			name: "fallback to legacy key",
			api:  APIConfig{ActiveProvider: "glm", APIKey: "legacy"},
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

	api.SetProviderKey("kimi", "km-new")
	if api.KimiKey != "km-new" {
		t.Errorf("SetProviderKey(kimi) failed: got %q", api.KimiKey)
	}

	api.SetProviderKey("glm", "glm-new")
	if api.GLMKey != "glm-new" {
		t.Errorf("SetProviderKey(glm) failed: got %q", api.GLMKey)
	}

	// Unknown provider should not panic
	api.SetProviderKey("nonexistent", "key")
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// v0.69 switched default to kimi-for-coding (Kimi Coding Plan).
	if cfg.Model.Name != "kimi-for-coding" {
		t.Errorf("Model.Name = %q, want kimi-for-coding", cfg.Model.Name)
	}
	if cfg.Model.Temperature != 0.6 {
		t.Errorf("Model.Temperature = %v, want 0.6", cfg.Model.Temperature)
	}
	if cfg.Model.MaxOutputTokens != 32768 {
		t.Errorf("Model.MaxOutputTokens = %v, want 32768", cfg.Model.MaxOutputTokens)
	}
	if cfg.API.Backend != "kimi" {
		t.Errorf("API.Backend = %q, want kimi", cfg.API.Backend)
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
