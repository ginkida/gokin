package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestMCP_YAMLEnabledMappingMinimal pins the simplest user flow: the
// docs-style snippet `mcp:\n  enabled: true` must populate Config.MCP.Enabled.
// Regression for the user-reported "I set mcp.enabled: true and nothing
// happened" — the fix is downstream (builder bootstraps an empty manager),
// but if the YAML→struct mapping ever silently breaks (typo in `yaml:"…"`
// tag, refactor that renames the field, …), this test catches it before
// users do.
func TestMCP_YAMLEnabledMappingMinimal(t *testing.T) {
	const src = `
mcp:
  enabled: true
`
	cfg := DefaultConfig()
	if err := yaml.Unmarshal([]byte(src), cfg); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if !cfg.MCP.Enabled {
		t.Fatal("mcp.enabled: true did not set MCP.Enabled")
	}
	if len(cfg.MCP.Servers) != 0 {
		t.Fatalf("expected 0 servers, got %d", len(cfg.MCP.Servers))
	}
}

// TestMCP_YAMLFullServerMapping pins every field-level YAML tag on
// MCPServerConfig so a rename of any tag (or accidentally adding
// `yaml:"-"`) trips this test.
func TestMCP_YAMLFullServerMapping(t *testing.T) {
	const src = `
mcp:
  enabled: true
  health_check_interval: 45s
  servers:
    - name: my-server
      transport: stdio
      command: my-cmd
      args:
        - --flag
        - value
      env:
        FOO: bar
      auto_connect: true
      timeout: 10s
      max_retries: 3
      retry_delay: 1s
      tool_prefix: my_
      permission_level: low
`
	cfg := DefaultConfig()
	if err := yaml.Unmarshal([]byte(src), cfg); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}
	if !cfg.MCP.Enabled {
		t.Fatal("MCP.Enabled = false, want true")
	}
	if cfg.MCP.HealthCheckInterval.Seconds() != 45 {
		t.Fatalf("HealthCheckInterval = %v, want 45s", cfg.MCP.HealthCheckInterval)
	}
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(cfg.MCP.Servers))
	}
	s := cfg.MCP.Servers[0]
	if s.Name != "my-server" {
		t.Errorf("Name = %q, want my-server", s.Name)
	}
	if s.Transport != "stdio" {
		t.Errorf("Transport = %q, want stdio", s.Transport)
	}
	if s.Command != "my-cmd" {
		t.Errorf("Command = %q, want my-cmd", s.Command)
	}
	if len(s.Args) != 2 || s.Args[0] != "--flag" || s.Args[1] != "value" {
		t.Errorf("Args = %v, want [--flag value]", s.Args)
	}
	if s.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want bar", s.Env["FOO"])
	}
	if !s.AutoConnect {
		t.Error("AutoConnect = false, want true")
	}
	if s.Timeout.Seconds() != 10 {
		t.Errorf("Timeout = %v, want 10s", s.Timeout)
	}
	if s.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", s.MaxRetries)
	}
	if s.RetryDelay.Seconds() != 1 {
		t.Errorf("RetryDelay = %v, want 1s", s.RetryDelay)
	}
	if s.ToolPrefix != "my_" {
		t.Errorf("ToolPrefix = %q, want my_", s.ToolPrefix)
	}
	if s.PermissionLevel != "low" {
		t.Errorf("PermissionLevel = %q, want low", s.PermissionLevel)
	}
}

// TestMCP_DefaultConfigDisabled pins the safe default — out-of-the-box,
// MCP is off so first-run users don't get surprise tools.
func TestMCP_DefaultConfigDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MCP.Enabled {
		t.Fatal("DefaultConfig().MCP.Enabled should be false")
	}
	if cfg.MCP.HealthCheckInterval == 0 {
		t.Fatal("DefaultConfig().MCP.HealthCheckInterval should be set")
	}
}
