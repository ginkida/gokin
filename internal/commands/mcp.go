package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gokin/internal/config"
	"gokin/internal/mcp"
	"gokin/internal/permission"
)

// MCPCommand manages Model Context Protocol servers at runtime.
//
// Subcommands:
//
//	/mcp                                        → list (default)
//	/mcp list                                   → summary of all servers
//	/mcp status [NAME]                          → per-server detail
//	/mcp add NAME stdio CMD [ARG ...]           → add stdio server
//	/mcp add NAME http URL                      → add http server
//	/mcp remove NAME                            → disconnect + remove
//	/mcp refresh NAME                           → re-list tools from a server
//	/mcp help                                   → usage
type MCPCommand struct{}

func (c *MCPCommand) Name() string        { return "mcp" }
func (c *MCPCommand) Description() string { return "Manage MCP (Model Context Protocol) servers" }
func (c *MCPCommand) Usage() string {
	return `/mcp                          - List all configured servers (default)
/mcp list                     - Summary of all servers
/mcp status [NAME]            - Per-server detail (all if NAME omitted)
/mcp add NAME stdio CMD [ARG] - Add a stdio server (persists to config)
/mcp add NAME http URL        - Add an http server (persists to config)
/mcp remove NAME              - Disconnect and remove a server
/mcp refresh NAME             - Re-fetch the tool list from a server
/mcp help                     - Show this help`
}

func (c *MCPCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "plug",
		Priority: 65,
		HasArgs:  true,
		ArgHint:  "[list|status|add|remove|refresh|help]",
	}
}

func (c *MCPCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetMCPManager()
	if mgr == nil {
		return "MCP is disabled. Set `mcp.enabled: true` in config to use this command.", nil
	}

	if len(args) == 0 {
		return mcpList(mgr), nil
	}
	switch strings.ToLower(args[0]) {
	case "list":
		return mcpList(mgr), nil
	case "status":
		return mcpStatus(mgr, args[1:]), nil
	case "add":
		return mcpAdd(ctx, mgr, app, args[1:])
	case "remove", "rm", "delete":
		return mcpRemove(mgr, app, args[1:])
	case "refresh":
		return mcpRefresh(ctx, mgr, args[1:])
	case "help", "-h", "--help":
		return c.Usage(), nil
	default:
		return fmt.Sprintf("Unknown subcommand %q. Run /mcp help.", args[0]), nil
	}
}

// ─── list ──────────────────────────────────────────────────────────────────

func mcpList(mgr *mcp.Manager) string {
	statuses := mgr.GetServerStatus()
	if len(statuses) == 0 {
		return "No MCP servers configured. Add one with `/mcp add NAME stdio CMD ...`."
	}

	// Stable order for predictable UI.
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	var sb strings.Builder
	sb.WriteString("MCP servers:\n")
	for _, s := range statuses {
		sb.WriteString("  ")
		sb.WriteString(formatServerLine(s))
		sb.WriteByte('\n')
	}

	connected := 0
	totalTools := 0
	for _, s := range statuses {
		if s.Connected {
			connected++
		}
		totalTools += s.ToolCount
	}
	fmt.Fprintf(&sb, "\n%d/%d connected, %d tools exposed to the model.",
		connected, len(statuses), totalTools)
	return sb.String()
}

// formatServerLine formats one server entry for /mcp list. Stable contract —
// tests assert against this.
func formatServerLine(s *mcp.ServerStatus) string {
	indicator := "✗ offline"
	switch {
	case s.Connected && s.Healthy:
		indicator = "✓ healthy"
	case s.Connected && !s.Healthy:
		indicator = "⚠ unhealthy"
	}
	return fmt.Sprintf("%-20s %s (%d tools)", s.Name, indicator, s.ToolCount)
}

// ─── status ────────────────────────────────────────────────────────────────

func mcpStatus(mgr *mcp.Manager, args []string) string {
	statuses := mgr.GetServerStatus()
	if len(args) > 0 {
		name := args[0]
		for _, s := range statuses {
			if s.Name == name {
				return formatServerDetail(s)
			}
		}
		return fmt.Sprintf("No MCP server named %q. Run /mcp list.", name)
	}

	if len(statuses) == 0 {
		return "No MCP servers configured."
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	var sb strings.Builder
	for i, s := range statuses {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(formatServerDetail(s))
	}
	return sb.String()
}

func formatServerDetail(s *mcp.ServerStatus) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Server: %s\n", s.Name)
	fmt.Fprintf(&sb, "  Connected:  %v\n", s.Connected)
	fmt.Fprintf(&sb, "  Healthy:    %v\n", s.Healthy)
	if s.ServerInfo != nil && s.ServerInfo.Version != "" {
		fmt.Fprintf(&sb, "  Version:    %s\n", s.ServerInfo.Version)
	}
	fmt.Fprintf(&sb, "  Tools (%d):", s.ToolCount)
	if s.ToolCount == 0 {
		sb.WriteString(" (none)\n")
	} else {
		sb.WriteByte('\n')
		names := append([]string(nil), s.ToolNames...)
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&sb, "    - %s\n", n)
		}
	}
	return sb.String()
}

// ─── add ───────────────────────────────────────────────────────────────────

// Exposed for tests — stub the config-save path.
var saveConfig = func(cfg *config.Config) error { return cfg.Save() }

func mcpAdd(ctx context.Context, mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) < 3 {
		return "Usage: /mcp add NAME stdio CMD [ARG ...] | /mcp add NAME http URL", nil
	}
	name := args[0]
	transport := strings.ToLower(args[1])

	if _, exists := mgr.GetServerConfig(name); exists {
		return fmt.Sprintf("Server %q already exists. Remove it first with /mcp remove.", name), nil
	}

	// New 3rd-party servers default to "high" risk so the user always sees a
	// prompt for an untrusted server's first call. Users can lower this in
	// YAML after they've vetted the server.
	serverCfg := &mcp.ServerConfig{
		Name:            name,
		Transport:       transport,
		AutoConnect:     true,
		PermissionLevel: "high",
	}
	yamlCfg := config.MCPServerConfig{
		Name:            name,
		Transport:       transport,
		AutoConnect:     true,
		PermissionLevel: "high",
	}

	switch transport {
	case "stdio":
		serverCfg.Command = args[2]
		yamlCfg.Command = args[2]
		if len(args) > 3 {
			serverCfg.Args = append([]string(nil), args[3:]...)
			yamlCfg.Args = append([]string(nil), args[3:]...)
		}
	case "http":
		serverCfg.URL = args[2]
		yamlCfg.URL = args[2]
	default:
		return fmt.Sprintf("Unknown transport %q. Use stdio or http.", transport), nil
	}

	if err := mgr.AddServer(serverCfg); err != nil {
		return "", fmt.Errorf("add server: %w", err)
	}

	// Connect and register tools. If connection fails, roll back the config
	// registration so the user isn't left with an invisible broken server.
	if err := mgr.Connect(ctx, name); err != nil {
		_ = mgr.RemoveServer(name)
		return "", fmt.Errorf("connect: %w", err)
	}

	if err := registerMCPTools(app, mgr, name, serverCfg.PermissionLevel); err != nil {
		// Best-effort cleanup.
		_ = mgr.Disconnect(name)
		_ = mgr.RemoveServer(name)
		return "", fmt.Errorf("register tools: %w", err)
	}

	// Persist to YAML so next startup reconnects automatically.
	cfg := app.GetConfig()
	cfg.MCP.Enabled = true
	cfg.MCP.Servers = append(cfg.MCP.Servers, yamlCfg)
	if err := saveConfig(cfg); err != nil {
		// Runtime state is fine; surface the persistence failure.
		return fmt.Sprintf("Server %q connected, but config save failed: %v", name, err), nil
	}

	toolCount := countToolsForServer(mgr, name)
	return fmt.Sprintf("Added MCP server %q (%d tools).", name, toolCount), nil
}

// ─── remove ────────────────────────────────────────────────────────────────

func mcpRemove(mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /mcp remove NAME", nil
	}
	name := args[0]

	if _, exists := mgr.GetServerConfig(name); !exists {
		return fmt.Sprintf("No MCP server named %q.", name), nil
	}

	if err := mgr.Disconnect(name); err != nil {
		return "", fmt.Errorf("disconnect: %w", err)
	}
	if err := mgr.RemoveServer(name); err != nil {
		return "", fmt.Errorf("remove server: %w", err)
	}

	// Source of truth for what needs unregistering is the registry itself —
	// tools/list_changed notifications could have added or removed tools
	// between the command starting and Disconnect completing, so a snapshot
	// from mgr.GetTools() taken earlier would be stale. The registry is
	// authoritative for what the LLM currently sees.
	reg := app.GetToolRegistry()
	var unregistered int
	if reg != nil {
		for _, t := range reg.List() {
			mt, ok := t.(*mcp.MCPTool)
			if !ok || mt.GetServerName() != name {
				continue
			}
			tn := mt.Name()
			if reg.Unregister(tn) {
				permission.ClearToolRiskOverride(tn)
				unregistered++
			}
		}
	}
	if c := app.GetMainClient(); c != nil && reg != nil {
		c.SetTools(reg.GeminiTools())
	}

	// Persist the removal.
	cfg := app.GetConfig()
	cfg.MCP.Servers = filterOutServer(cfg.MCP.Servers, name)
	if err := saveConfig(cfg); err != nil {
		return fmt.Sprintf("Removed MCP server %q at runtime, but config save failed: %v", name, err), nil
	}

	return fmt.Sprintf("Removed MCP server %q (%d tools unregistered).", name, unregistered), nil
}

// ─── refresh ───────────────────────────────────────────────────────────────

func mcpRefresh(ctx context.Context, mgr *mcp.Manager, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /mcp refresh NAME", nil
	}
	name := args[0]
	if err := mgr.RefreshTools(ctx, name); err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		return fmt.Sprintf("Refresh failed for %q: %v", name, err), nil
	}
	toolCount := countToolsForServer(mgr, name)
	return fmt.Sprintf("Refreshed %q (%d tools).", name, toolCount), nil
}

// ─── helpers ───────────────────────────────────────────────────────────────

// registerMCPTools pulls the manager's current tool list and registers only
// tools belonging to the named server. Idempotent — Registry.Register would
// return an error on duplicates, which we ignore so reruns are safe.
//
// permLevel is the server's configured permission level ("low"/"medium"/
// "high"). Each registered tool gets a matching risk override so the
// permission layer treats it according to the server's trust tier instead
// of the default RiskMedium fallback.
func registerMCPTools(app AppInterface, mgr *mcp.Manager, serverName, permLevel string) error {
	reg := app.GetToolRegistry()
	if reg == nil {
		return fmt.Errorf("tool registry unavailable")
	}
	level := permission.ParseRiskLevel(permLevel)
	for _, t := range mgr.GetTools() {
		mt, ok := t.(*mcp.MCPTool)
		if !ok || mt.GetServerName() != serverName {
			continue
		}
		if err := reg.Register(t); err != nil {
			// Already registered — not fatal.
			continue
		}
		permission.SetToolRiskOverride(t.Name(), level)
	}
	if c := app.GetMainClient(); c != nil {
		c.SetTools(reg.GeminiTools())
	}
	return nil
}

func countToolsForServer(mgr *mcp.Manager, serverName string) int {
	n := 0
	for _, t := range mgr.GetTools() {
		if mt, ok := t.(*mcp.MCPTool); ok && mt.GetServerName() == serverName {
			n++
		}
	}
	return n
}

func filterOutServer(servers []config.MCPServerConfig, name string) []config.MCPServerConfig {
	out := make([]config.MCPServerConfig, 0, len(servers))
	for _, s := range servers {
		if s.Name != name {
			out = append(out, s)
		}
	}
	return out
}
