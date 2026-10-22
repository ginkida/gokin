package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"gokin/internal/tools"
)

// MCPTool wraps an MCP server tool as a Gokin tool.
type MCPTool struct {
	client      *Client                    // MCP client connection
	serverName  string                     // Server name for identification
	toolName    string                     // Original MCP tool name
	displayName string                     // Prefixed name for Gemini
	description string                     // Tool description
	inputSchema *JSONSchema                // MCP input schema
	declaration *genai.FunctionDeclaration // Cached declaration
	claudeName  string
}

// NewMCPTool creates a new MCPTool wrapper.
func NewMCPTool(client *Client, serverName, prefix string, toolInfo *ToolInfo) *MCPTool {
	// Determine the effective prefix for naming
	effectivePrefix := prefix
	if effectivePrefix == "" && serverName != "" {
		effectivePrefix = serverName
	}

	displayName := toolInfo.Name
	if effectivePrefix != "" {
		displayName = effectivePrefix + "_" + toolInfo.Name
	}

	// Sanitize for Gemini
	displayName = sanitizeFunctionName(displayName)

	// Create declaration with the same effective prefix for consistency
	declaration := ConvertMCPToolToDeclaration(toolInfo, effectivePrefix)
	claudeName := "mcp__" + serverName + "__" + toolInfo.Name
	if client != nil && client.config != nil && client.config.pluginToolPrefix != "" {
		claudeName = client.config.pluginToolPrefix + toolInfo.Name
		displayName = pluginMCPFunctionName(claudeName, serverName+"\x00"+toolInfo.Name)
		declaration.Name = displayName
	}

	return &MCPTool{
		client:      client,
		serverName:  serverName,
		toolName:    toolInfo.Name,
		displayName: displayName,
		description: toolInfo.Description,
		inputSchema: toolInfo.InputSchema,
		declaration: declaration,
		claudeName:  claudeName,
	}
}

// Name returns the tool name as registered in Gokin.
func (t *MCPTool) Name() string {
	return t.displayName
}

// Description returns the tool description.
func (t *MCPTool) Description() string {
	return t.description
}

// Declaration returns the Gemini function declaration.
func (t *MCPTool) Declaration() *genai.FunctionDeclaration {
	return t.declaration
}

// Validate validates the tool arguments.
func (t *MCPTool) Validate(args map[string]any) error {
	if t.inputSchema == nil {
		return nil
	}

	// Validate required fields
	for _, required := range t.inputSchema.Required {
		if _, ok := args[required]; !ok {
			return tools.NewValidationError(required, "is required")
		}
	}

	// Validate property types if schema has properties
	if t.inputSchema.Properties != nil {
		for name, schema := range t.inputSchema.Properties {
			if val, ok := args[name]; ok {
				if err := validateValue(name, val, schema); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

// validateValue validates a value against a JSON schema.
func validateValue(name string, val any, schema *JSONSchema) error {
	if schema == nil {
		return nil
	}

	switch schema.Type {
	case "string":
		if _, ok := val.(string); !ok {
			return tools.NewValidationError(name, "must be a string")
		}
	case "number", "integer":
		switch val.(type) {
		case int, int64, float64:
			// OK
		case json.Number:
			if _, err := val.(json.Number).Float64(); err != nil {
				return tools.NewValidationError(name, "must be a number")
			}
		default:
			return tools.NewValidationError(name, "must be a number")
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return tools.NewValidationError(name, "must be a boolean")
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return tools.NewValidationError(name, "must be an array")
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return tools.NewValidationError(name, "must be an object")
		}
	}

	return nil
}

// Execute runs the MCP tool.
func (t *MCPTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	if t.client == nil {
		return tools.NewErrorResult("MCP server not connected"), nil
	}
	if err := t.client.config.CheckReady(ctx); err != nil {
		if tools.IsHookStopError(err) {
			return tools.NewHookStoppedResult(err.Error()), nil
		}
		return tools.NewErrorResult(err.Error()), nil
	}

	if !t.client.IsInitialized() {
		return tools.NewErrorResult("MCP server not initialized"), nil
	}

	// Call the MCP tool
	result, err := t.client.CallTool(ctx, t.toolName, args)
	if err != nil {
		if tools.IsHookStopError(err) {
			return tools.NewHookStoppedResult(err.Error()), nil
		}
		return tools.NewErrorResult(fmt.Sprintf("MCP call failed: %s", err)), nil
	}

	// Check if the tool returned an error
	if result.IsError {
		errMsg := formatContentBlocks(result.Content)
		return tools.NewErrorResult(errMsg), nil
	}

	// Format the result content
	content := formatContentBlocks(result.Content)

	return tools.NewSuccessResultWithData(content, map[string]any{
		"mcp_server":  t.serverName,
		"mcp_tool":    t.toolName,
		"raw_content": result.Content,
	}), nil
}

// formatContentBlocks formats MCP content blocks into a string.
func formatContentBlocks(blocks []*ContentBlock) string {
	if len(blocks) == 0 {
		return "(no output)"
	}

	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		case "image":
			parts = append(parts, fmt.Sprintf("[Image: %s]", block.MIMEType))
		case "resource":
			parts = append(parts, fmt.Sprintf("[Resource: %s]", block.URI))
		default:
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
	}

	if len(parts) == 0 {
		return "(no output)"
	}

	return strings.Join(parts, "\n")
}

// GetServerName returns the MCP server name.
func (t *MCPTool) GetServerName() string {
	return t.serverName
}

// GetOriginalToolName returns the original MCP tool name.
func (t *MCPTool) GetOriginalToolName() string {
	return t.toolName
}

func (t *MCPTool) ClaudeToolName() string { return t.claudeName }

func pluginMCPFunctionName(name, identity string) string {
	valid := len(name) > 0 && len(name) <= 64
	for _, c := range name {
		valid = valid && (c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
	}
	if valid {
		return name
	}
	alias := sanitizeFunctionName(name)
	if len(alias) > 47 {
		alias = alias[:47]
	}
	sum := sha256.Sum256([]byte(identity))
	return alias + "_" + hex.EncodeToString(sum[:8])
}

// GetClient returns the MCP client.
func (t *MCPTool) GetClient() *Client {
	return t.client
}
