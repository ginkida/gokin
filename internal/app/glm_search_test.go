package app

import "testing"

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
