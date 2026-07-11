package app

import (
	"testing"

	"gokin/internal/config"
	"gokin/internal/security"
)

// TestGLMWebSearchServer pins the auto-wired GLM Coding Plan web search MCP
// server config: the verified endpoint, the GLM key injected as the Bearer
// Authorization header, http transport, and low (read-only first-party) risk.
func TestGLMWebSearchServer(t *testing.T) {
	s := glmWebSearchServer("my-glm-key")
	if s.Name != "web-search-prime" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Transport != "http" {
		t.Errorf("Transport = %q, want http", s.Transport)
	}
	if s.URL != "https://api.z.ai/api/mcp/web_search_prime/mcp" {
		t.Errorf("URL = %q", s.URL)
	}
	if got := s.Headers["Authorization"]; got != "Bearer my-glm-key" {
		t.Errorf("Authorization header = %q, want 'Bearer my-glm-key'", got)
	}
	if !s.AutoConnect {
		t.Error("AutoConnect should be true")
	}
	if s.PermissionLevel != "low" {
		t.Errorf("PermissionLevel = %q, want low (read-only first-party search)", s.PermissionLevel)
	}
}

func TestResolveGLMKeyMatchesClientPrecedence(t *testing.T) {
	t.Setenv("GOKIN_GLM_KEY", "")
	t.Setenv("GLM_API_KEY", "")

	tests := []struct {
		name       string
		configKey  string
		legacyKey  string
		envKey     string
		want       string
		wantSource security.KeySource
	}{
		{"provider config", "config-glm-key", "legacy-key", "", "config-glm-key", security.KeySourceConfig},
		{"legacy fallback", "", "legacy-key", "", "legacy-key", security.KeySourceConfig},
		{"environment wins", "config-glm-key", "legacy-key", "env-glm-key", "env-glm-key", security.KeySourceEnvironment},
		{"unset", "", "", "", "", security.KeySourceNotSet},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GOKIN_GLM_KEY", tt.envKey)
			cfg := config.DefaultConfig()
			cfg.API.GLMKey = tt.configKey
			cfg.API.APIKey = tt.legacyKey

			got := resolveGLMKey(cfg)
			if got.Value != tt.want || got.Source != tt.wantSource {
				t.Fatalf("resolveGLMKey() = value %q source %q, want %q %q",
					got.Value, got.Source, tt.want, tt.wantSource)
			}
		})
	}
}

func TestResolveGLMKeyNilConfig(t *testing.T) {
	got := resolveGLMKey(nil)
	if got.IsSet() {
		t.Fatalf("resolveGLMKey(nil) unexpectedly returned a key from %s", got.Source)
	}
}
