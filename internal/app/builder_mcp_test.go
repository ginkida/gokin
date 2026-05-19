package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
)

// TestInitMCP_DisabledLeavesManagerNil pins the kill-switch: `mcp.enabled: false`
// must not create a manager, otherwise `/mcp` would show "configured" when the
// YAML explicitly says off.
func TestInitMCP_DisabledLeavesManagerNil(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = false

	b := &Builder{cfg: cfg, ctx: context.Background()}
	if err := b.initMCP(); err != nil {
		t.Fatalf("initMCP returned error: %v", err)
	}
	if b.mcpManager != nil {
		t.Fatal("mcp manager should stay nil when mcp.enabled=false")
	}
}

// TestInitMCP_EnabledWithoutServersBootstrapsManager is the regression for the
// user-reported bug: flipping `mcp.enabled: true` in YAML used to skip the
// MCP init entirely because the guard required `len(Servers) > 0`. That meant
// `/mcp add NAME stdio CMD …` couldn't bootstrap from scratch — the command
// would (incorrectly) say MCP was disabled.
//
// After the fix, an empty `mcp.servers` list still produces a manager, so the
// user can add their first server at runtime without editing YAML first.
func TestInitMCP_EnabledWithoutServersBootstrapsManager(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = true
	cfg.MCP.Servers = nil

	b := &Builder{cfg: cfg, ctx: context.Background()}
	if err := b.initMCP(); err != nil {
		t.Fatalf("initMCP returned error: %v", err)
	}
	if b.mcpManager == nil {
		t.Fatal("mcp manager should be created when mcp.enabled=true even with empty servers")
	}
	if got := len(b.mcpManager.GetTools()); got != 0 {
		t.Fatalf("empty manager should expose 0 tools, got %d", got)
	}
	if got := len(b.mcpManager.GetConnectedServers()); got != 0 {
		t.Fatalf("empty manager should have 0 connected servers, got %d", got)
	}
	// User-facing summary should hint at next step, not be empty-and-silent.
	if !strings.Contains(b.mcpConnectSummary, "/mcp add") {
		t.Fatalf("empty-but-enabled summary should mention `/mcp add`, got %q", b.mcpConnectSummary)
	}
}

// TestInitMCP_EnabledWithServersCreatesManager verifies the original
// (with-servers) path is still exercised after refactor — config rows convert
// to mcp.ServerConfig and the manager surfaces them. Servers have
// auto_connect=false so we don't actually try to spawn subprocesses in the
// unit test.
func TestInitMCP_EnabledWithServersCreatesManager(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = true
	cfg.MCP.Servers = []config.MCPServerConfig{
		{
			Name:            "test-server",
			Transport:       "stdio",
			Command:         "echo",
			AutoConnect:     false,
			PermissionLevel: "low",
		},
	}

	b := &Builder{cfg: cfg, ctx: context.Background()}
	if err := b.initMCP(); err != nil {
		t.Fatalf("initMCP returned error: %v", err)
	}
	if b.mcpManager == nil {
		t.Fatal("manager should be created")
	}
	statuses := b.mcpManager.GetServerStatus()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 server in manager, got %d", len(statuses))
	}
	if statuses[0].Name != "test-server" {
		t.Fatalf("server name = %q, want %q", statuses[0].Name, "test-server")
	}
	// auto_connect=false → no connect attempt → summary stays empty.
	if b.mcpConnectSummary != "" {
		t.Fatalf("auto_connect=false should leave summary empty, got %q", b.mcpConnectSummary)
	}
}
