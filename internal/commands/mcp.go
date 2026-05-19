package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/mcp"
	"gokin/internal/permission"
	"gokin/internal/tools"
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
		// `add` and `refresh` spawn a stdio subprocess + JSON-RPC handshake
		// or hit a remote HTTP endpoint, routinely 2–10s on real servers.
		// `list`/`status` are local and fast, but the toast clears on
		// command completion so the cost is negligible. Better one extra
		// flicker than the user retrying /mcp add and getting "server
		// already exists" mid-handshake.
		LongRunning:      true,
		LongRunningLabel: "Talking to MCP server — stdio handshake or HTTP roundtrip in flight...",
	}
}

func (c *MCPCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetMCPManager()
	if mgr == nil {
		// Manager is nil only when `mcp.enabled: false` (or unset). When the
		// flag is true the builder constructs an empty manager so `/mcp add`
		// can bootstrap from scratch.
		return "MCP is disabled. Set `mcp.enabled: true` in config (then restart) " +
			"to use this command, or use the setup wizard.", nil
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

// ─── list / status ─────────────────────────────────────────────────────────
//
// The actual rendering lives in `mcp.FormatList` / `mcp.FormatStatus` so the
// model-facing `mcp_admin` tool (internal/tools/mcp_admin.go) can produce the
// same exact text without importing this package (which would cycle).

func mcpList(mgr *mcp.Manager) string {
	return mcp.FormatList(mgr)
}

func mcpStatus(mgr *mcp.Manager, args []string) string {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	return mcp.FormatStatus(mgr, name)
}

// ─── add ───────────────────────────────────────────────────────────────────

// Exposed for tests — stub the config-save path.
var saveConfig = func(cfg *config.Config) error { return cfg.Save() }

// MCPAddParams is the structured input to MCPAddCore — same fields that
// /mcp add parses from its positional args, in a form callable from the
// model-facing mcp_admin tool too.
type MCPAddParams struct {
	Name      string   // Required. Unique identifier.
	Transport string   // Required. "stdio" or "http".
	Command   string   // stdio only. Subprocess to launch.
	Args      []string // stdio only. Subprocess args.
	URL       string   // http only. Server endpoint.
}

// MCPAddCore is the shared engine behind /mcp add and the model-facing
// mcp_admin{action:add} tool. Both call sites get identical validation,
// rollback-on-error, permission tier, and config persistence.
//
// On success returns a one-line "Added MCP server …" summary. On invalid
// input returns a user-facing usage hint with nil error (so the caller
// can surface it to the user as a normal message, not a stack trace).
// On infrastructure failure (subprocess exec, network) returns the error
// after rolling back the manager state.
func MCPAddCore(
	ctx context.Context,
	mgr *mcp.Manager,
	cfg *config.Config,
	reg *tools.Registry,
	mainClient client.Client,
	p MCPAddParams,
) (string, error) {
	if mgr == nil {
		return "MCP is not initialised. Enable it in config first.", nil
	}
	if p.Name == "" || p.Transport == "" {
		return "Usage: name + transport (stdio|http) are required.", nil
	}
	transport := strings.ToLower(p.Transport)

	if _, exists := mgr.GetServerConfig(p.Name); exists {
		return fmt.Sprintf("Server %q already exists. Remove it first with /mcp remove.", p.Name), nil
	}

	// New 3rd-party servers default to "high" risk so the user always sees a
	// prompt for an untrusted server's first call. Users can lower this in
	// YAML after they've vetted the server.
	serverCfg := &mcp.ServerConfig{
		Name:            p.Name,
		Transport:       transport,
		AutoConnect:     true,
		PermissionLevel: "high",
	}
	yamlCfg := config.MCPServerConfig{
		Name:            p.Name,
		Transport:       transport,
		AutoConnect:     true,
		PermissionLevel: "high",
	}

	switch transport {
	case "stdio":
		if p.Command == "" {
			return "stdio transport requires `command`.", nil
		}
		serverCfg.Command = p.Command
		yamlCfg.Command = p.Command
		if len(p.Args) > 0 {
			serverCfg.Args = append([]string(nil), p.Args...)
			yamlCfg.Args = append([]string(nil), p.Args...)
		}
	case "http":
		if p.URL == "" {
			return "http transport requires `url`.", nil
		}
		serverCfg.URL = p.URL
		yamlCfg.URL = p.URL
	default:
		return fmt.Sprintf("Unknown transport %q. Use stdio or http.", p.Transport), nil
	}

	if err := mgr.AddServer(serverCfg); err != nil {
		return "", fmt.Errorf("add server: %w", err)
	}

	// Connect and register tools. If connection fails, roll back the config
	// registration so the user isn't left with an invisible broken server.
	if err := mgr.Connect(ctx, p.Name); err != nil {
		_ = mgr.RemoveServer(p.Name)
		return "", fmt.Errorf("connect: %w", err)
	}

	if err := registerMCPToolsByRegistry(reg, mgr, p.Name, serverCfg.PermissionLevel); err != nil {
		// Best-effort cleanup.
		_ = mgr.Disconnect(p.Name)
		_ = mgr.RemoveServer(p.Name)
		return "", fmt.Errorf("register tools: %w", err)
	}
	if mainClient != nil && reg != nil {
		mainClient.SetTools(reg.GeminiTools())
	}

	// Persist to YAML so next startup reconnects automatically.
	if cfg != nil {
		cfg.MCP.Enabled = true
		cfg.MCP.Servers = append(cfg.MCP.Servers, yamlCfg)
		if err := saveConfig(cfg); err != nil {
			// Runtime state is fine; surface the persistence failure.
			return fmt.Sprintf("Server %q connected, but config save failed: %v", p.Name, err), nil
		}
	}

	toolCount := countToolsForServer(mgr, p.Name)
	return fmt.Sprintf("Added MCP server %q (%d tools).", p.Name, toolCount), nil
}

func mcpAdd(ctx context.Context, mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) < 3 {
		return "Usage: /mcp add NAME stdio CMD [ARG ...] | /mcp add NAME http URL", nil
	}
	p := MCPAddParams{
		Name:      args[0],
		Transport: strings.ToLower(args[1]),
	}
	switch p.Transport {
	case "stdio":
		p.Command = args[2]
		if len(args) > 3 {
			p.Args = append([]string(nil), args[3:]...)
		}
	case "http":
		p.URL = args[2]
	default:
		return fmt.Sprintf("Unknown transport %q. Use stdio or http.", args[1]), nil
	}
	return MCPAddCore(ctx, mgr, app.GetConfig(), app.GetToolRegistry(), app.GetMainClient(), p)
}

// ─── remove ────────────────────────────────────────────────────────────────

// MCPRemoveCore is the shared engine behind /mcp remove and the model-facing
// mcp_admin{action:remove} tool. Disconnects the server, drops its tools
// from the registry, and persists the removal.
func MCPRemoveCore(
	mgr *mcp.Manager,
	cfg *config.Config,
	reg *tools.Registry,
	mainClient client.Client,
	name string,
) (string, error) {
	if mgr == nil {
		return "MCP is not initialised.", nil
	}
	if name == "" {
		return "Server name is required.", nil
	}
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
	if mainClient != nil && reg != nil {
		mainClient.SetTools(reg.GeminiTools())
	}

	// Persist the removal.
	if cfg != nil {
		cfg.MCP.Servers = filterOutServer(cfg.MCP.Servers, name)
		if err := saveConfig(cfg); err != nil {
			return fmt.Sprintf("Removed MCP server %q at runtime, but config save failed: %v", name, err), nil
		}
	}

	return fmt.Sprintf("Removed MCP server %q (%d tools unregistered).", name, unregistered), nil
}

func mcpRemove(mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /mcp remove NAME", nil
	}
	return MCPRemoveCore(mgr, app.GetConfig(), app.GetToolRegistry(), app.GetMainClient(), args[0])
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

// registerMCPTools is the App-side wrapper used by the /mcp add slash command.
// It pulls the manager's current tool list and registers only tools belonging
// to the named server. Idempotent — Registry.Register would return an error
// on duplicates, which we ignore so reruns are safe.
//
// permLevel is the server's configured permission level ("low"/"medium"/
// "high"). Each registered tool gets a matching risk override so the
// permission layer treats it according to the server's trust tier instead
// of the default RiskMedium fallback.
func registerMCPTools(app AppInterface, mgr *mcp.Manager, serverName, permLevel string) error {
	return registerMCPToolsByRegistry(app.GetToolRegistry(), mgr, serverName, permLevel)
}

// registerMCPToolsByRegistry is the primitive form used by MCPAddCore so the
// model-facing add path doesn't need an AppInterface. Behaviour is identical
// to registerMCPTools.
func registerMCPToolsByRegistry(reg *tools.Registry, mgr *mcp.Manager, serverName, permLevel string) error {
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
