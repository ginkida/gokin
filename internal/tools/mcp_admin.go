package tools

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

// MCPAddParams is the structured input the tool passes to its add callback.
// Mirror of commands.MCPAddParams but defined here so the tool package can
// be the source of truth for its own wire format. Builder maps between the
// two when wiring callbacks.
type MCPAddParams struct {
	Name      string   // Required.
	Transport string   // "stdio" or "http".
	Command   string   // stdio only.
	Args      []string // stdio only.
	URL       string   // http only.
}

// MCPAdminTool gives the model read/write access to the MCP control plane so
// it can answer questions like "is MCP set up?" AND act on commands like
// "add a github MCP server" without forcing the user to type /mcp themselves.
//
// Implementation note: the tool can't import `internal/mcp` because that
// package already imports `internal/tools` (for the Tool interface) — going
// the other way would cycle. So the actual renderers + mutators are injected
// as callbacks by app/builder.go, which has both packages in scope. When MCP
// is disabled or the callbacks are unset the tool returns a setup-hint
// instead of erroring.
type MCPAdminTool struct {
	list          func() string
	status        func(name string) string
	add           func(ctx context.Context, params MCPAddParams) (string, error)
	remove        func(name string) (string, error)
	enabled       func() bool
	listResources func() string
	readResource  func(ctx context.Context, uri string) (string, error)
	listPrompts   func() string
	getPrompt     func(ctx context.Context, name string, arguments map[string]any) (string, error)

	// Control-plane callbacks (v0.101+): enable/disable, pause/resume, edit,
	// preset. Each returns a human-readable result string. When nil, Execute
	// reports the op as unwired rather than panicking.
	toggleEnabled func(enable bool) (string, error)
	setPaused     func(name string, paused bool) (string, error)
	editServer    func(ctx context.Context, params MCPAddParams) (string, error)
	addPreset     func(name string) (string, error)
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

// SetMutationCallbacks wires the write callbacks for add/remove. Kept
// separate from SetCallbacks so tests can exercise read-only behaviour
// without supplying mutators.
func (t *MCPAdminTool) SetMutationCallbacks(
	addFn func(ctx context.Context, params MCPAddParams) (string, error),
	removeFn func(name string) (string, error),
) {
	t.add = addFn
	t.remove = removeFn
}

// SetControlCallbacks wires the enable/disable, pause/resume, edit, and
// preset callbacks (v0.101+). Each returns a human-readable result string.
// Kept separate from SetMutationCallbacks so a build or test that only needs
// add/remove can leave these nil — Execute then reports the op as unwired.
func (t *MCPAdminTool) SetControlCallbacks(
	toggleEnabledFn func(enable bool) (string, error),
	setPausedFn func(name string, paused bool) (string, error),
	editServerFn func(ctx context.Context, params MCPAddParams) (string, error),
	addPresetFn func(name string) (string, error),
) {
	t.toggleEnabled = toggleEnabledFn
	t.setPaused = setPausedFn
	t.editServer = editServerFn
	t.addPreset = addPresetFn
}

// SetResourceCallbacks wires the MCP resources read-path: listResourcesFn
// renders a catalog of resources exposed across all servers, readResourceFn
// fetches one resource's contents by URI. Separate setter so a build without
// resource support (or a test) can leave them nil — Execute then reports the
// op as unwired rather than panicking.
func (t *MCPAdminTool) SetResourceCallbacks(
	listResourcesFn func() string,
	readResourceFn func(ctx context.Context, uri string) (string, error),
) {
	t.listResources = listResourcesFn
	t.readResource = readResourceFn
}

// SetPromptCallbacks wires the MCP prompts read-path: listPromptsFn renders a
// catalog of prompt templates across all servers, getPromptFn fetches one
// rendered prompt by name (with optional arguments). Separate setter so a build
// without prompt support (or a test) can leave them nil — Execute then reports
// the op as unwired rather than panicking.
func (t *MCPAdminTool) SetPromptCallbacks(
	listPromptsFn func() string,
	getPromptFn func(ctx context.Context, name string, arguments map[string]any) (string, error),
) {
	t.listPrompts = listPromptsFn
	t.getPrompt = getPromptFn
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
		Description: "Inspect or modify MCP (Model Context Protocol) server configuration. Use this when the user asks about MCP setup or wants to add/remove MCP servers from chat.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"action": {
					Type: genai.TypeString,
					Description: "What to do. " +
						"'list' (default) — short status line per server, plus how many tools are exposed to the model. " +
						"'status' — detailed view; pass `server` to focus one. " +
						"'help' — usage cheatsheet to share with the user. " +
						"'add' — register a new MCP server; pass `server`, `transport`, plus `command`+`args` for stdio or `url` for http. " +
						"'remove' — disconnect + drop a server by `server` name. " +
						"'enable'/'disable' — turn MCP support on/off at runtime (no restart). " +
						"'pause'/'resume' — suppress auto-connect for one `server` without removing it. " +
						"'edit' — update an existing `server`'s command/url/args in place. " +
						"'preset' — add a popular server from the built-in catalog; pass `server` as the preset name (e.g. github, filesystem). " +
						"'resources' — list context resources exposed by MCP servers; pass `uri` to read one resource's contents. " +
						"'prompts' — list prompt templates exposed by MCP servers; pass `name` to fetch one rendered prompt (with optional `prompt_args`).",
					Enum: []string{"list", "status", "help", "add", "remove", "enable", "disable", "pause", "resume", "edit", "preset", "resources", "prompts"},
				},
				"server": {
					Type:        genai.TypeString,
					Description: "Server name. Required for action=status (focus one server), action=add, action=remove. Omitted for list shows everything.",
				},
				"uri": {
					Type:        genai.TypeString,
					Description: "Resource URI for action=resources. Provide it to read that resource's contents; omit to list every available resource.",
				},
				"name": {
					Type:        genai.TypeString,
					Description: "Prompt name for action=prompts. Provide it to fetch that prompt's rendered messages; omit to list every available prompt.",
				},
				"prompt_args": {
					Type:        genai.TypeObject,
					Description: "Optional arguments for action=prompts when fetching a named prompt (key/value pairs the prompt template declares).",
				},
				"transport": {
					Type:        genai.TypeString,
					Description: "Transport kind for action=add. 'stdio' spawns a local subprocess (use with `command` + optional `args`); 'http' hits a remote URL (use with `url`).",
					Enum:        []string{"stdio", "http"},
				},
				"command": {
					Type:        genai.TypeString,
					Description: "Subprocess to launch for stdio transport. Example: 'npx'.",
				},
				"args": {
					Type:        genai.TypeArray,
					Items:       &genai.Schema{Type: genai.TypeString},
					Description: "Arguments passed to `command` for stdio transport. Example: ['-y', '@modelcontextprotocol/server-github'].",
				},
				"url": {
					Type:        genai.TypeString,
					Description: "Endpoint URL for http transport. Example: 'https://example.com/mcp'.",
				},
			},
		},
	}
}

func (t *MCPAdminTool) Validate(args map[string]any) error {
	action := GetStringDefault(args, "action", "list")
	switch action {
	case "list", "status", "help", "resources", "prompts":
		return nil
	case "add":
		if GetStringDefault(args, "server", "") == "" {
			return fmt.Errorf("action=add requires `server` (server name)")
		}
		transport := GetStringDefault(args, "transport", "")
		switch transport {
		case "stdio":
			if GetStringDefault(args, "command", "") == "" {
				return fmt.Errorf("action=add transport=stdio requires `command`")
			}
		case "http":
			if GetStringDefault(args, "url", "") == "" {
				return fmt.Errorf("action=add transport=http requires `url`")
			}
		case "":
			return fmt.Errorf("action=add requires `transport` (stdio or http)")
		default:
			return fmt.Errorf("action=add: unknown transport %q (use stdio or http)", transport)
		}
		return nil
	case "remove":
		if GetStringDefault(args, "server", "") == "" {
			return fmt.Errorf("action=remove requires `server` (server name)")
		}
		return nil
	case "enable", "disable":
		return nil // no server arg — toggles global MCP support
	case "pause", "resume":
		if GetStringDefault(args, "server", "") == "" {
			return fmt.Errorf("action=%s requires `server` (server name)", action)
		}
		return nil
	case "edit":
		if GetStringDefault(args, "server", "") == "" {
			return fmt.Errorf("action=edit requires `server` (existing server name)")
		}
		return nil // command/url/args are optional — only provided fields are updated
	case "preset":
		if GetStringDefault(args, "server", "") == "" {
			return fmt.Errorf("action=preset requires `server` (preset name, e.g. github)")
		}
		return nil
	default:
		return fmt.Errorf("unknown action %q (use list, status, help, add, remove, enable, disable, pause, resume, edit, preset, resources, or prompts)", action)
	}
}

func (t *MCPAdminTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	action := GetStringDefault(args, "action", "list")

	// help and enable are reachable even when MCP is disabled — the model
	// often asks for the cheatsheet, and enable is the whole point of
	// bootstrapping MCP from an off state.
	if action == "help" {
		return NewSuccessResult(mcpAdminHelpText()), nil
	}
	if action == "enable" {
		if t.toggleEnabled == nil {
			return NewErrorResult("mcp_admin enable is not wired in this build"), nil
		}
		out, err := t.toggleEnabled(true)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
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
	case "add":
		if t.add == nil {
			return NewErrorResult("mcp_admin add is not wired in this build"), nil
		}
		params := MCPAddParams{
			Name:      GetStringDefault(args, "server", ""),
			Transport: GetStringDefault(args, "transport", ""),
			Command:   GetStringDefault(args, "command", ""),
			URL:       GetStringDefault(args, "url", ""),
		}
		if raw, ok := args["args"].([]any); ok {
			for _, a := range raw {
				if s, ok := a.(string); ok {
					params.Args = append(params.Args, s)
				}
			}
		} else if raw, ok := args["args"].([]string); ok {
			params.Args = append([]string(nil), raw...)
		}
		out, err := t.add(ctx, params)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "remove":
		if t.remove == nil {
			return NewErrorResult("mcp_admin remove is not wired in this build"), nil
		}
		out, err := t.remove(GetStringDefault(args, "server", ""))
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "disable":
		if t.toggleEnabled == nil {
			return NewErrorResult("mcp_admin disable is not wired in this build"), nil
		}
		out, err := t.toggleEnabled(false)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "pause":
		if t.setPaused == nil {
			return NewErrorResult("mcp_admin pause is not wired in this build"), nil
		}
		out, err := t.setPaused(GetStringDefault(args, "server", ""), true)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "resume":
		if t.setPaused == nil {
			return NewErrorResult("mcp_admin resume is not wired in this build"), nil
		}
		out, err := t.setPaused(GetStringDefault(args, "server", ""), false)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "edit":
		if t.editServer == nil {
			return NewErrorResult("mcp_admin edit is not wired in this build"), nil
		}
		params := MCPAddParams{
			Name:      GetStringDefault(args, "server", ""),
			Transport: GetStringDefault(args, "transport", ""),
			Command:   GetStringDefault(args, "command", ""),
			URL:       GetStringDefault(args, "url", ""),
		}
		if raw, ok := args["args"].([]any); ok {
			for _, a := range raw {
				if s, ok := a.(string); ok {
					params.Args = append(params.Args, s)
				}
			}
		} else if raw, ok := args["args"].([]string); ok {
			params.Args = append([]string(nil), raw...)
		}
		out, err := t.editServer(ctx, params)
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "preset":
		if t.addPreset == nil {
			return NewErrorResult("mcp_admin preset is not wired in this build"), nil
		}
		out, err := t.addPreset(GetStringDefault(args, "server", ""))
		if err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult(out), nil
	case "resources":
		uri := GetStringDefault(args, "uri", "")
		if uri != "" {
			if t.readResource == nil {
				return NewErrorResult("mcp_admin resource read is not wired in this build"), nil
			}
			out, err := t.readResource(ctx, uri)
			if err != nil {
				return NewErrorResult(err.Error()), nil
			}
			return NewSuccessResult(out), nil
		}
		if t.listResources == nil {
			return NewErrorResult("mcp_admin resources is not wired in this build"), nil
		}
		return NewSuccessResult(t.listResources()), nil
	case "prompts":
		name := GetStringDefault(args, "name", "")
		if name != "" {
			if t.getPrompt == nil {
				return NewErrorResult("mcp_admin prompt fetch is not wired in this build"), nil
			}
			var promptArgs map[string]any
			if raw, ok := args["prompt_args"].(map[string]any); ok {
				promptArgs = raw
			}
			out, err := t.getPrompt(ctx, name, promptArgs)
			if err != nil {
				return NewErrorResult(err.Error()), nil
			}
			return NewSuccessResult(out), nil
		}
		if t.listPrompts == nil {
			return NewErrorResult("mcp_admin prompts is not wired in this build"), nil
		}
		return NewSuccessResult(t.listPrompts()), nil
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

3. Add a server. Either path works:
   a) From chat, call this tool: mcp_admin{action:add, server:"github",
        transport:"stdio", command:"npx",
        args:["-y", "@modelcontextprotocol/server-github"]}.
      This runs end-to-end without the user typing anything — the new
      server is connected, its tools are registered, and the change is
      persisted to the config.
   b) From chat as the user, slash-command style:
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

   The tool exposes the same: mcp_admin{action:list}, {action:status},
   {action:remove, server:"github"}.

   To inspect server-provided context: mcp_admin{action:resources} lists
   context resources (read one with {action:resources, uri:"..."}), and
   mcp_admin{action:prompts} lists prompt templates (fetch one rendered with
   {action:prompts, name:"...", prompt_args:{...}}).

5. As the model, prefer calling mcp_admin{action:list} first to see what is
   already configured before suggesting an add.`
}
