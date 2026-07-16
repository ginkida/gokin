package permission

import (
	"context"
	"strings"
	"testing"
)

// --- RememberWithArgs (0% → 100%) ---

func TestRememberWithArgs(t *testing.T) {
	m := NewManager(nil, true)
	m.RememberWithArgs("read", map[string]any{"file_path": "/tmp/test"}, DecisionAllow)
	if m == nil {
		t.Fatal("manager should not be nil")
	}
}

func TestRememberWithArgs_ThenCheck(t *testing.T) {
	m := NewManager(nil, true)
	args := map[string]any{"file_path": "/tmp/test"}
	m.RememberWithArgs("read", args, DecisionAllow)

	// After RememberWithArgs, Check should return the cached decision
	// without needing to ask the user
	resp, err := m.Check(context.Background(), "read", args)
	if err != nil {
		t.Fatalf("Check after RememberWithArgs: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Decision != DecisionAllow {
		t.Fatalf("expected DecisionAllow, got %v", resp.Decision)
	}
}

// --- buildMCPAdminReason (66.7% → 100%) ---

func TestBuildMCPAdminReason_AddStdio(t *testing.T) {
	args := map[string]any{
		"action":    "add",
		"server":    "myserver",
		"transport": "stdio",
		"command":   "npx -y @mcp/server",
	}
	got := buildMCPAdminReason(args)
	if !strings.Contains(got, "spawns local subprocess") {
		t.Fatalf("expected 'spawns local subprocess' in reason, got %q", got)
	}
	if !strings.Contains(got, "myserver") {
		t.Fatalf("expected server name in reason, got %q", got)
	}
}

func TestBuildMCPAdminReason_AddHTTP(t *testing.T) {
	args := map[string]any{
		"action":    "add",
		"server":    "myserver",
		"transport": "http",
		"url":       "https://api.example.com/mcp",
	}
	got := buildMCPAdminReason(args)
	if !strings.Contains(got, "connects to") {
		t.Fatalf("expected 'connects to' in reason, got %q", got)
	}
	if !strings.Contains(got, "https://api.example.com/mcp") {
		t.Fatalf("expected URL in reason, got %q", got)
	}
}

func TestBuildMCPAdminReason_AddUnknownTransport(t *testing.T) {
	args := map[string]any{
		"action":    "add",
		"server":    "myserver",
		"transport": "unknown",
	}
	got := buildMCPAdminReason(args)
	if !strings.Contains(got, "Add MCP server") {
		t.Fatalf("expected 'Add MCP server' in reason, got %q", got)
	}
	if strings.Contains(got, "spawns") || strings.Contains(got, "connects") {
		t.Fatalf("unknown transport should not mention spawns/connects, got %q", got)
	}
}

func TestBuildMCPAdminReason_Remove(t *testing.T) {
	args := map[string]any{
		"action": "remove",
		"server": "myserver",
	}
	got := buildMCPAdminReason(args)
	if !strings.Contains(got, "Remove MCP server") {
		t.Fatalf("expected 'Remove MCP server' in reason, got %q", got)
	}
}

func TestBuildMCPAdminReason_DefaultAction(t *testing.T) {
	args := map[string]any{}
	got := buildMCPAdminReason(args)
	if !strings.Contains(got, "MCP admin: list") {
		t.Fatalf("expected default 'list' action, got %q", got)
	}
}

func TestBuildMCPAdminReason_OtherAction(t *testing.T) {
	args := map[string]any{"action": "status"}
	got := buildMCPAdminReason(args)
	if !strings.Contains(got, "MCP admin: status") {
		t.Fatalf("expected 'MCP admin: status', got %q", got)
	}
}

// --- buildReason additional paths (80.8% → higher) ---

func TestBuildReason_BashTool(t *testing.T) {
	got := buildReason("bash", map[string]any{"command": "ls -la"})
	if !strings.Contains(got, "ls -la") {
		t.Fatalf("expected command in reason, got %q", got)
	}
}

func TestBuildReason_UnknownTool(t *testing.T) {
	got := buildReason("unknown_tool", map[string]any{"key": "value"})
	if got == "" {
		t.Fatal("expected non-empty reason for unknown tool")
	}
}

// --- cacheKey (80% → 100%) ---

func TestCacheKey_NilArgs(t *testing.T) {
	m := NewManager(nil, true)
	key1 := m.cacheKey("read", nil)
	key2 := m.cacheKey("read", nil)
	if key1 != key2 {
		t.Fatal("same tool + nil args should produce same key")
	}
}

func TestCacheKey_DifferentArgs(t *testing.T) {
	m := NewManager(nil, true)
	key1 := m.cacheKey("read", map[string]any{"path": "/a"})
	key2 := m.cacheKey("read", map[string]any{"path": "/b"})
	if key1 == key2 {
		t.Fatal("different args should produce different keys")
	}
}

// --- cloneRules (83.3% → 100%) ---

func TestCloneRules_Empty(t *testing.T) {
	rules := &Rules{}
	clone := cloneRules(rules)
	if clone == nil {
		t.Fatal("clone should not be nil")
	}
}

// --- clonePolicyOverrides (83.3% → 100%) ---

func TestClonePolicyOverrides_Empty(t *testing.T) {
	// clonePolicyOverrides(nil) returns nil for empty input
	if got := clonePolicyOverrides(nil); got != nil {
		t.Fatalf("clonePolicyOverrides(nil) = %v, want nil", got)
	}

	// With actual overrides, returns a clone
	src := map[string]Level{"read": "low"}
	clone := clonePolicyOverrides(src)
	if clone == nil || clone["read"] != "low" {
		t.Fatalf("clone with data = %v, want copy of %v", clone, src)
	}
}

// --- SetRules (85.7% → 100%) ---

func TestSetRules_NilRules(t *testing.T) {
	m := NewManager(nil, true)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("SetRules(nil) panicked: %v", r)
		}
	}()
	m.SetRules(nil)
}

// --- isEnvAssignment (85.7% → 100%) ---

func TestIsEnvAssignment_ValidAssignment(t *testing.T) {
	// isEnvAssignment only accepts lowercase letters, digits, underscore
	if !isEnvAssignment("foo=bar") {
		t.Fatal("foo=bar should be an env assignment")
	}
	if !isEnvAssignment("_path=/tmp") {
		t.Fatal("_path=/tmp should be an env assignment")
	}
	if !isEnvAssignment("var_123=value") {
		t.Fatal("var_123=value should be an env assignment")
	}
}

func TestIsEnvAssignment_NoEquals(t *testing.T) {
	if isEnvAssignment("echo hello") {
		t.Fatal("echo hello should not be an env assignment")
	}
}

func TestIsEnvAssignment_Empty(t *testing.T) {
	if isEnvAssignment("") {
		t.Fatal("empty string should not be an env assignment")
	}
}

// --- pipesRemoteToShell (83.3% → 100%) ---

func TestPipesRemoteToShell_NoPipe(t *testing.T) {
	if pipesRemoteToShell("echo hello") {
		t.Fatal("echo hello should not pipe to shell")
	}
}

func TestPipesRemoteToShell_LocalPipe(t *testing.T) {
	if pipesRemoteToShell("cat file | grep foo") {
		t.Fatal("local pipe should not be remote-to-shell")
	}
}
