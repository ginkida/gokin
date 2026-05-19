package tools

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

// MCPAdminTool gives the model read-only access to the MCP control plane so
// it can answer questions like "is MCP set up?", "which servers do I have?"
// or "show me what's wrong with the github MCP server" without forcing the
// user to type `/mcp list` themselves.
//
// Write actions (add/remove) are intentionally NOT here yet — they require
// persistence + tool-registry sync that the read-only path doesn't need.
// They land in a follow-up once the inspection path proves out.
//
// Implementation note: the tool can't import `internal/mcp` because that
// package already imports `internal/tools` (for the Tool interface) — going
// the other way would cycle. So the actual renderers are injected as
// callbacks by app/builder.go, which has both packages in scope. When MCP
// is disabled or the callbacks are unset the tool returns a setup-hint
// instead of erroring.
type MCPAdminTool struct {
	list     func() string
	status   func(name string) string
	enabled  func() bool
}

// NewMCPAdminTool constructs the tool with no callbacks attached. The builder
// wires the manager via SetCallbacks after MCP initialisation completes.
func NewMCPAdminTool() *MCPAdminTool {
	return &MCPAdminTool{}
}

// SetCallbacks wires the inspection callbacks. Any nil callback is treated as
// "MCP not initialised" by Execute. `enabledFn` exists so the tool can
// distinguish "MCP off in config" from "MCP on but no servers yet".
func (t *MCPAdminTool) SetCallbacks(listFn func() string, statusFn func(name string) string, enabledFn func() bool) {
	t.list = listFn
	t.status = statusFn
	t.enabled = enabledFn
}

func (t *MCPAdminTool) Name() string {
	return "mcp_admin"
}

func (t *MCPAdminTool) Description() string {
	return "Inspect MCP (Model Context Protocol) server state. Use this when the user asks about MCP setup, mentions adding/removing MCP servers, or asks why an MCP-backed tool isn't appearing. Read-only; safe to call freely."
}

func (t *MCPAdminTool) Declaration() *genai.FunctionDeclaration {
	return MCPAdminToolDeclaration()
}

// MCPAdminToolDeclaration is the static schema, exported so it lives in the
// declarations map alongside every other tool.
func MCPAdminToolDeclaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "mcp_admin",
		Description: "Inspect MCP (Model Context Protocol) server state. Read-only.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type: genai.TypeString,
					Description: "What to do. " +
						"'list' (default) — short status line per server, plus how many tools are exposed to the model. " +
						"'status' — detailed view; pass `server` to focus one. " +
						"'help' — usage cheatsheet to share with the user when they want to add/remove MCP servers.",
					Enum: []string{"list", "status", "help"},
				},
				"server": {
					Type:        genai.TypeString,
					Description: "Server name. Only used with action=status; if omitted, status shows every server.",
				},
			},
		},
	}
}

func (t *MCPAdminTool) Validate(args map[string]any) error {
	action := GetStringDefault(args, "action", "list")
	switch action {
	case "list", "status", "help":
		return nil
	default:
		return fmt.Errorf("unknown action %q (use list, status, or help)", action)
	}
}

func (t *MCPAdminTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	action := GetStringDefault(args, "action", "list")

	// help is reachable even when MCP is disabled — the model often asks for
	// the cheatsheet so it can paste a `/mcp add` snippet to the user.
	if action == "help" {
		return NewSuccessResult(mcpAdminHelpText()), nil
	}

	if !t.mcpReady() {
		return NewSuccessResult(t.disabledHint()), nil
	}

	switch action {
	case "list":
		return NewSuccessResult(t.list()), nil
	case "status":
		server := GetStringDefault(args, "server", "")
		return NewSuccessResult(t.status(server)), nil
	default:
		return NewErrorResult(fmt.Sprintf("unknown action %q", action)), nil
	}
}

// mcpReady reports whether the inspection callbacks have been wired AND the
// runtime considers MCP enabled. Either side missing means we route to the
// disabled-hint path rather than NPE.
func (t *MCPAdminTool) mcpReady() bool {
	if t == nil || t.list == nil || t.status == nil {
		return false
	}
	if t.enabled == nil {
		return true // callbacks set without an enabled predicate — trust them.
	}
	return t.enabled()
}

// disabledHint returns the message shown when MCP isn't wired. We surface a
// concrete next step so the model can quote it to the user verbatim.
func (t *MCPAdminTool) disabledHint() string {
	return "MCP is currently disabled. Tell the user to set `mcp.enabled: true` " +
		"in their config (~/.config/gokin/config.yaml or platform equivalent) and restart gokin, " +
		"then run `/mcp add NAME stdio CMD …` to register a server. " +
		"Call mcp_admin with action=help for the full cheatsheet."
}

// mcpAdminHelpText is the cheatsheet returned by action=help. Kept in code
// (not a yaml/markdown file) so the model can always quote it verbatim.
func mcpAdminHelpText() string {
	return `MCP setup cheatsheet:

1. Enable MCP in the user's config file (one-time):
     ~/.config/gokin/config.yaml
   Add or edit this block:
     mcp:
       enabled: true
       health_check_interval: 30s

2. Restart gokin so the manager initialises.

3. Add a server from the chat with /mcp add:
     /mcp add github stdio npx -y @modelcontextprotocol/server-github
     /mcp add filesystem stdio npx -y @modelcontextprotocol/server-filesystem /allowed/dir
     /mcp add my-api http https://example.com/mcp

   Stdio servers spawn a local subprocess; http servers hit a remote URL.
   New servers default to permission_level=high so the user is prompted on
   first call from the model — they can lower in YAML once vetted.

4. Useful commands:
     /mcp list                       summary of all servers
     /mcp status [NAME]              detailed view
     /mcp refresh NAME               re-fetch tool list
     /mcp remove NAME                disconnect + drop

5. As the model, prefer calling mcp_admin{action:list} first to see what is
   already configured before suggesting /mcp add.`
}
