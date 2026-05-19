package app

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/tools"
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

// TestInitMCP_WiresAdminTool pins the link between MCP init and the
// model-facing inspection tool. Without this, the model's `mcp_admin{list}`
// call would silently return the "MCP is disabled" hint even when MCP is
// actually enabled — exactly the symptom the user reported about the loop
// task that spawned this work.
func TestInitMCP_WiresAdminTool(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = true

	b := &Builder{
		cfg:      cfg,
		ctx:      context.Background(),
		registry: tools.DefaultRegistry(t.TempDir()),
	}
	if err := b.initMCP(); err != nil {
		t.Fatalf("initMCP returned error: %v", err)
	}

	tool, ok := b.registry.Get("mcp_admin")
	if !ok {
		t.Fatal("mcp_admin tool not in registry")
	}
	admin, ok := tool.(*tools.MCPAdminTool)
	if !ok {
		t.Fatalf("registry returned wrong type for mcp_admin: %T", tool)
	}

	res, err := admin.Execute(context.Background(), map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("admin.Execute returned err: %v", err)
	}
	// After wiring, list output should come from mcp.FormatList(b.mcpManager)
	// — with no servers that's the "No MCP servers configured" hint.
	if !strings.Contains(res.Content, "No MCP servers configured") {
		t.Fatalf("after wiring, list output should be from FormatList, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "MCP is currently disabled") {
		t.Fatal("callbacks were not wired — tool fell back to disabled-hint")
	}
}
