package tools

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// ToolsListTool returns a list of all available tools in the registry.
type ToolsListTool struct {
	baseRegistry *Registry
	lister       ToolLister // For lazy registry support
}

// NewToolsListTool creates a new tools_list tool.
func NewToolsListTool(registry *Registry) *ToolsListTool {
	return &ToolsListTool{baseRegistry: registry}
}

// NewToolsListToolLazy creates a new tools_list tool with ToolLister interface.
// This avoids cyclic dependency when used with LazyRegistry.
func NewToolsListToolLazy(lister ToolLister) *ToolsListTool {
	return &ToolsListTool{lister: lister}
}

func (t *ToolsListTool) Name() string {
	return "tools_list"
}

func (t *ToolsListTool) Description() string {
	return "Returns a list of all available tools in the system with their descriptions. Use this to discover tools you don't currently have access to. Optional 'server' argument filters to MCP-provided tools from a specific server."
}

func (t *ToolsListTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"server": {
					Type:        genai.TypeString,
					Description: "Optional: filter to MCP-provided tools from this server name (as shown in /mcp list). Omit to list all tools.",
				},
			},
		},
	}
}

func (t *ToolsListTool) Validate(args map[string]any) error {
	return nil
}

// mcpServerSource is satisfied by MCP-sourced tools that know their originating
// server. Defined inline to avoid importing internal/mcp here.
type mcpServerSource interface {
	GetServerName() string
}

func (t *ToolsListTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	serverFilter, _ := args["server"].(string)
	serverFilter = strings.TrimSpace(serverFilter)

	var output strings.Builder
	if serverFilter != "" {
		fmt.Fprintf(&output, "MCP tools from server %q:\n\n", serverFilter)
	} else {
		output.WriteString("Available tools in the system:\n\n")
	}

	matched := 0
	switch {
	case t.baseRegistry != nil:
		// Eager registry path — we have Tool objects, so per-server filter works.
		tools := t.baseRegistry.List()
		for _, tool := range tools {
			if serverFilter != "" {
				src, ok := tool.(mcpServerSource)
				if !ok || src.GetServerName() != serverFilter {
					continue
				}
			}
			fmt.Fprintf(&output, "- **%s**: %s\n", tool.Name(), tool.Description())
			matched++
		}
	case t.lister != nil:
		// Lazy registry path — only have declarations, no tool objects.
		// Server filtering isn't supported here. Surface that limitation
		// instead of silently returning everything.
		if serverFilter != "" {
			return NewErrorResult(
				"Server filtering is not available in lazy tool-registry mode. " +
					"Run without the 'server' argument to see all tools, or use /mcp status to inspect a specific server.",
			), nil
		}
		for _, decl := range t.lister.Declarations() {
			desc := decl.Description
			if runes := []rune(desc); len(runes) > 100 {
				desc = string(runes[:97]) + "..."
			}
			fmt.Fprintf(&output, "- **%s**: %s\n", decl.Name, desc)
			matched++
		}
	default:
		return NewErrorResult("no registry or lister configured"), nil
	}

	if serverFilter != "" && matched == 0 {
		return NewSuccessResult(fmt.Sprintf(
			"No tools found for MCP server %q. Run /mcp list to see configured servers.",
			serverFilter,
		)), nil
	}

	// Main-agent sessions don't get request_tool anymore (it was
	// demoted to ToolSetAgent in v0.70.2 because its dependency isn't
	// wired outside the sub-agent path). Only advertise it when the
	// current toolkit actually includes it — otherwise the hint tells
	// Kimi to call a tool it doesn't have, which is exactly the
	// silent-failure pattern we're trying to eliminate.
	if t.hasRequestTool() {
		output.WriteString("\nIf you need a tool that is not in your current toolkit, use 'request_tool' to request it.")
	}
	return NewSuccessResult(output.String()), nil
}

// hasRequestTool returns true when request_tool is in the current
// toolkit. Used to gate the hint at the end of tools_list output so
// main-agent responses don't advertise a sub-agent-only capability.
// Works against both the eager Registry and the LazyRegistry/ToolLister
// interfaces — whichever is wired for this instance.
func (t *ToolsListTool) hasRequestTool() bool {
	if t.baseRegistry != nil {
		_, ok := t.baseRegistry.Get("request_tool")
		return ok
	}
	if t.lister != nil {
		for _, name := range t.lister.Names() {
			if name == "request_tool" {
				return true
			}
		}
	}
	return false
}
