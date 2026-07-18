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
//	/mcp enable                                 → turn MCP on at runtime (no restart)
//	/mcp disable                                → turn MCP off at runtime
//	/mcp add NAME stdio CMD [ARG ...]           → add stdio server
//	/mcp add NAME http URL                      → add http server
//	/mcp remove NAME                            → disconnect + remove
//	/mcp pause NAME                             → disconnect but keep config
//	/mcp resume NAME                            → reconnect a paused server
//	/mcp edit NAME command=CMD args=A,B         → modify a server in-place
//	/mcp refresh NAME                           → re-list tools from a server
//	/mcp preset                                 → list popular server presets
//	/mcp preset NAME                            → add a server from the catalog
//	/mcp setup                                  → guided first-time setup
//	/mcp help                                   → usage
type MCPCommand struct{}

func (c *MCPCommand) Name() string        { return "mcp" }
func (c *MCPCommand) Description() string { return "Manage MCP (Model Context Protocol) servers" }
func (c *MCPCommand) Usage() string {
	return `/mcp                          - List all configured servers (default)
/mcp list                     - Summary of all servers
/mcp status [NAME]            - Per-server detail (all if NAME omitted)
/mcp enable                   - Turn MCP on at runtime (no config edit/restart)
/mcp disable                  - Turn MCP off and disconnect all servers
/mcp add NAME stdio CMD [ARG] - Add a stdio server (persists to config)
/mcp add NAME http URL        - Add an http server (persists to config)
/mcp remove NAME              - Disconnect and remove a server
/mcp pause NAME               - Pause a server (disconnect, keep config)
/mcp resume NAME              - Resume a paused server (reconnect)
/mcp edit NAME command=CMD    - Edit a server in-place (command=, args=, url=)
/mcp refresh NAME             - Re-fetch the tool list from a server
/mcp preset                   - List popular MCP server presets
/mcp preset NAME              - Add a server from the preset catalog
/mcp setup                    - Guided first-time MCP setup
/mcp help                     - Show this help`
}

func (c *MCPCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "plug",
		Priority: 65,
		HasArgs:  true,
		ArgHint:  "[list|status|enable|disable|add|remove|pause|resume|edit|refresh|preset|setup|help]",
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
	// Subcommands that work even when MCP is disabled (they create/destroy
	// the manager themselves) are handled before the nil-check below.
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "enable":
			return mcpEnable(app)
		case "disable":
			return mcpDisable(app)
		case "setup":
			return mcpSetup(ctx, app, args[1:])
		case "preset":
			// preset list works without a manager; preset add needs one
			if len(args) < 2 || strings.ToLower(args[1]) == "list" {
				return mcpPresetList(), nil
			}
		}
	}

	mgr := app.GetMCPManager()
	if mgr == nil {
		// Manager is nil only when `mcp.enabled: false` (or unset). When the
		// flag is true the builder constructs an empty manager so `/mcp add`
		// can bootstrap from scratch.
		return "MCP is disabled. Run `/mcp enable` to turn it on, or `/mcp setup` for guided setup.", nil
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
	case "pause":
		return mcpPause(ctx, mgr, app, args[1:])
	case "resume":
		return mcpResume(ctx, mgr, app, args[1:])
	case "edit":
		return mcpEdit(ctx, mgr, app, args[1:])
	case "refresh":
		return mcpRefresh(ctx, mgr, args[1:])
	case "preset":
		return mcpPresetAdd(ctx, mgr, app, args[1:])
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

// mcpConfigSnapshotCommitter is implemented by the real App. GetConfig returns
// an isolated candidate, while MCPAddCore/MCPRemoveCore intentionally avoid a
// full provider rebuild; commit the candidate after those runtime operations.
type mcpConfigSnapshotCommitter interface {
	CommitMCPConfigSnapshot(cfg *config.Config)
}

// mcpConfigMutationLocker is optional so lightweight command test doubles do
// not need transaction machinery. The real App implements it, serializing the
// complete add/remove lifecycle while leaving list/status/refresh unaffected.
type mcpConfigMutationLocker interface {
	LockMCPConfigMutation()
	UnlockMCPConfigMutation()
}

func lockMCPConfigMutation(app AppInterface) func() {
	locker, ok := app.(mcpConfigMutationLocker)
	if !ok {
		return func() {}
	}
	locker.LockMCPConfigMutation()
	return locker.UnlockMCPConfigMutation
}

func commitMCPConfigSnapshot(app AppInterface, cfg *config.Config) {
	if committer, ok := app.(mcpConfigSnapshotCommitter); ok {
		committer.CommitMCPConfigSnapshot(cfg)
	}
}

// MCPAddForApp runs the shared MCP add engine against an isolated App config
// candidate and commits only the MCP section when runtime state changed.
func MCPAddForApp(ctx context.Context, mgr *mcp.Manager, app AppInterface, p MCPAddParams) (string, error) {
	unlock := lockMCPConfigMutation(app)
	defer unlock()

	cfg := app.GetConfig()
	beforeServers, beforeEnabled := 0, false
	if cfg != nil {
		beforeServers, beforeEnabled = len(cfg.MCP.Servers), cfg.MCP.Enabled
	}
	result, err := MCPAddCore(ctx, mgr, cfg, app.GetToolRegistry(), app.GetMainClient(), p)
	if err == nil && cfg != nil && (len(cfg.MCP.Servers) != beforeServers || cfg.MCP.Enabled != beforeEnabled) {
		commitMCPConfigSnapshot(app, cfg)
	}
	return result, err
}

// MCPRemoveForApp is the remove counterpart to MCPAddForApp.
func MCPRemoveForApp(mgr *mcp.Manager, app AppInterface, name string) (string, error) {
	unlock := lockMCPConfigMutation(app)
	defer unlock()

	cfg := app.GetConfig()
	beforeServers := 0
	if cfg != nil {
		beforeServers = len(cfg.MCP.Servers)
	}
	result, err := MCPRemoveCore(mgr, cfg, app.GetToolRegistry(), app.GetMainClient(), name)
	if err == nil && cfg != nil && len(cfg.MCP.Servers) != beforeServers {
		commitMCPConfigSnapshot(app, cfg)
	}
	return result, err
}

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
	return MCPAddForApp(ctx, mgr, app, p)
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
	return MCPRemoveForApp(mgr, app, args[0])
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

// ─── enable / disable ─────────────────────────────────────────────────────

func mcpEnable(app AppInterface) (string, error) {
	if err := app.EnableMCP(); err != nil {
		return "", err
	}
	return "MCP enabled. Use `/mcp add` or `/mcp preset` to add servers.", nil
}

func mcpDisable(app AppInterface) (string, error) {
	if err := app.DisableMCP(); err != nil {
		return "", err
	}
	return "MCP disabled. All MCP servers disconnected and tools removed.", nil
}

// ─── pause / resume ────────────────────────────────────────────────────────

func mcpPause(ctx context.Context, mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /mcp pause NAME", nil
	}
	name := args[0]
	if _, exists := mgr.GetServerConfig(name); !exists {
		return fmt.Sprintf("No MCP server named %q.", name), nil
	}
	// Disconnect if currently connected.
	_ = mgr.Disconnect(name)
	if err := mgr.SetPaused(name, true); err != nil {
		return "", err
	}
	// Unregister tools from the registry so the model stops seeing them.
	if reg := app.GetToolRegistry(); reg != nil {
		for _, t := range reg.List() {
			if mt, ok := t.(*mcp.MCPTool); ok && mt.GetServerName() == name {
				tn := mt.Name()
				if reg.Unregister(tn) {
					permission.ClearToolRiskOverride(tn)
				}
			}
		}
		if c := app.GetMainClient(); c != nil {
			c.SetTools(reg.GeminiTools())
		}
	}
	persistServerField(app, name, func(s *config.MCPServerConfig) { s.Paused = true })
	return fmt.Sprintf("Paused MCP server %q. Use `/mcp resume %s` to reconnect.", name, name), nil
}

func mcpResume(ctx context.Context, mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) == 0 {
		return "Usage: /mcp resume NAME", nil
	}
	name := args[0]
	if _, exists := mgr.GetServerConfig(name); !exists {
		return fmt.Sprintf("No MCP server named %q.", name), nil
	}
	if err := mgr.SetPaused(name, false); err != nil {
		return "", err
	}
	// Reconnect to pick up tools.
	if err := mgr.Connect(ctx, name); err != nil {
		persistServerField(app, name, func(s *config.MCPServerConfig) { s.Paused = false })
		return fmt.Sprintf("Resumed %q config but connection failed: %v", name, err), nil
	}
	// Re-register tools.
	cfg, _ := mgr.GetServerConfig(name)
	if cfg != nil {
		registerMCPToolsByRegistry(app.GetToolRegistry(), mgr, name, cfg.PermissionLevel)
		if c := app.GetMainClient(); c != nil {
			c.SetTools(app.GetToolRegistry().GeminiTools())
		}
	}
	persistServerField(app, name, func(s *config.MCPServerConfig) { s.Paused = false })
	toolCount := countToolsForServer(mgr, name)
	return fmt.Sprintf("Resumed MCP server %q (%d tools).", name, toolCount), nil
}

// ─── edit ──────────────────────────────────────────────────────────────────

func mcpEdit(ctx context.Context, mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) < 2 {
		return "Usage: /mcp edit NAME command=CMD [args=A,B] [url=URL] [transport=stdio|http]", nil
	}
	name := args[0]
	cfg, exists := mgr.GetServerConfig(name)
	if !exists {
		return fmt.Sprintf("No MCP server named %q.", name), nil
	}

	// Work on a copy.
	updated := *cfg
	changed := false
	for _, kv := range args[1:] {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Sprintf("Invalid edit %q — use key=value format.", kv), nil
		}
		key, val := strings.ToLower(parts[0]), parts[1]
		switch key {
		case "command":
			updated.Command = val
			changed = true
		case "args":
			updated.Args = strings.Split(val, ",")
			changed = true
		case "url":
			updated.URL = val
			changed = true
		case "transport":
			if val != "stdio" && val != "http" {
				return fmt.Sprintf("Invalid transport %q — use stdio or http.", val), nil
			}
			updated.Transport = val
			changed = true
		case "auto_connect":
			updated.AutoConnect = val == "true" || val == "1" || val == "yes"
			changed = true
		default:
			return fmt.Sprintf("Unknown edit key %q — valid: command, args, url, transport, auto_connect.", key), nil
		}
	}
	if !changed {
		return "No changes specified.", nil
	}

	if err := mgr.UpdateServer(&updated); err != nil {
		return "", err
	}
	// Disconnect + reconnect to pick up new settings.
	_ = mgr.Disconnect(name)
	if err := mgr.Connect(ctx, name); err != nil {
		return fmt.Sprintf("Updated %q but reconnect failed: %v", name, err), nil
	}
	// Re-register tools with potentially new definitions.
	if reg := app.GetToolRegistry(); reg != nil {
		for _, t := range reg.List() {
			if mt, ok := t.(*mcp.MCPTool); ok && mt.GetServerName() == name {
				reg.Unregister(mt.Name())
				permission.ClearToolRiskOverride(mt.Name())
			}
		}
		registerMCPToolsByRegistry(reg, mgr, name, updated.PermissionLevel)
		if c := app.GetMainClient(); c != nil {
			c.SetTools(reg.GeminiTools())
		}
	}
	// Persist to config.
	persistServerField(app, name, func(s *config.MCPServerConfig) {
		s.Command = updated.Command
		s.Args = updated.Args
		s.URL = updated.URL
		s.Transport = updated.Transport
		s.AutoConnect = updated.AutoConnect
	})
	toolCount := countToolsForServer(mgr, name)
	return fmt.Sprintf("Updated MCP server %q (%d tools).", name, toolCount), nil
}

// ─── preset ────────────────────────────────────────────────────────────────

func mcpPresetList() string {
	return FormatMCPPresets()
}

func mcpPresetAdd(ctx context.Context, mgr *mcp.Manager, app AppInterface, args []string) (string, error) {
	if len(args) == 0 {
		return FormatMCPPresets(), nil
	}
	presetName := strings.ToLower(args[0])
	preset, ok := LookupMCPPreset(presetName)
	if !ok {
		return fmt.Sprintf("Unknown preset %q. Run /mcp preset to see available presets.", presetName), nil
	}
	return MCPAddForApp(ctx, mgr, app, preset)
}

// ─── exported wrappers for mcp_admin tool (builder.go) ──────────────────────
// These mirror the slash-command handlers above but take structured args
// instead of raw []string, so the model-invocable mcp_admin tool can call
// them via builder.go's SetControlCallbacks wiring.

// MCPEnableForApp turns MCP support on at runtime.
func MCPEnableForApp(app AppInterface) (string, error) {
	return mcpEnable(app)
}

// MCPDisableForApp turns MCP support off and disconnects all servers.
func MCPDisableForApp(app AppInterface) (string, error) {
	return mcpDisable(app)
}

// MCPPauseForApp disconnects a server but keeps its config.
func MCPPauseForApp(ctx context.Context, mgr *mcp.Manager, app AppInterface, name string) (string, error) {
	return mcpPause(ctx, mgr, app, []string{name})
}

// MCPResumeForApp reconnects a paused server.
func MCPResumeForApp(ctx context.Context, mgr *mcp.Manager, app AppInterface, name string) (string, error) {
	return mcpResume(ctx, mgr, app, []string{name})
}

// MCPEditForApp updates an existing server's command/url/args in place.
func MCPEditForApp(ctx context.Context, mgr *mcp.Manager, app AppInterface, name string, p MCPAddParams) (string, error) {
	args := []string{name}
	if p.Command != "" {
		args = append(args, "command="+p.Command)
	}
	if len(p.Args) > 0 {
		args = append(args, "args="+strings.Join(p.Args, ","))
	}
	if p.URL != "" {
		args = append(args, "url="+p.URL)
	}
	if p.Transport != "" {
		args = append(args, "transport="+p.Transport)
	}
	return mcpEdit(ctx, mgr, app, args)
}

// MCPPresetAddForApp adds a server from the built-in preset catalog.
func MCPPresetAddForApp(ctx context.Context, mgr *mcp.Manager, app AppInterface, presetName string) (string, error) {
	return mcpPresetAdd(ctx, mgr, app, []string{presetName})
}

// ─── setup ─────────────────────────────────────────────────────────────────

func mcpSetup(ctx context.Context, app AppInterface, args []string) (string, error) {
	mgr := app.GetMCPManager()
	if mgr == nil {
		if err := app.EnableMCP(); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	b.WriteString("MCP Setup\n")
	b.WriteString("=========\n\n")
	b.WriteString("MCP is now enabled. Here are popular servers you can add:\n\n")
	b.WriteString(FormatMCPPresets())
	b.WriteString("\nTo add one, run e.g.:\n")
	b.WriteString("  /mcp preset github\n")
	b.WriteString("  /mcp preset filesystem\n\n")
	b.WriteString("Or add a custom server:\n")
	b.WriteString("  /mcp add myserver stdio npx -y @scope/mcp-server\n")
	b.WriteString("  /mcp add myapi http https://example.com/mcp\n")
	return b.String(), nil
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

// persistServerField mutates a named server's config entry in-place and saves.
// If the server isn't in the config (e.g. added at runtime but not yet
// persisted), the mutation is silently skipped — the runtime manager is the
// source of truth, config is the persistence layer.
func persistServerField(app AppInterface, name string, mutate func(*config.MCPServerConfig)) {
	cfg := app.GetConfig()
	if cfg == nil {
		return
	}
	for i := range cfg.MCP.Servers {
		if cfg.MCP.Servers[i].Name == name {
			mutate(&cfg.MCP.Servers[i])
			_ = saveConfig(cfg)
			return
		}
	}
}
