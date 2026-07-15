package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/agent"
)

// RegisterAgentTypeCommand registers a custom agent type.
type RegisterAgentTypeCommand struct{}

func (c *RegisterAgentTypeCommand) Name() string {
	return "register-agent-type"
}

func (c *RegisterAgentTypeCommand) Description() string {
	return "Register a custom agent type with specific tools"
}

func (c *RegisterAgentTypeCommand) Usage() string {
	return `/register-agent-type <name> "<description>" [--tools t1,t2,t3] [--prompt "text"]`
}

func (c *RegisterAgentTypeCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "robot",
		HasArgs:  true,
		ArgHint:  `<name> "<desc>" [--tools ...] [--prompt ...]`,
		Priority: 30,
		Advanced: true,
	}
}

func (c *RegisterAgentTypeCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}

	registry := app.GetAgentTypeRegistry()
	if registry == nil {
		return "", fmt.Errorf("agent type registry not available")
	}

	// Parse arguments
	name := args[0]
	description := ""
	var tools []string
	prompt := ""

	// Parse remaining args
	i := 1
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "--tools":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--tools requires a comma-separated value")
			}
			i++
			tools = cleanToolList(args[i])
		case arg == "--prompt":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--prompt requires a value")
			}
			i++
			prompt = args[i]
		case strings.HasPrefix(arg, "--"):
			return "", fmt.Errorf("unknown option: %s", arg)
		case description == "":
			// Remove quotes if present
			description = strings.Trim(arg, `"'`)
		}
		i++
	}

	if description == "" {
		return "", fmt.Errorf("description is required")
	}

	// Register the new agent type
	if err := registry.RegisterDynamic(name, description, tools, prompt); err != nil {
		return "", fmt.Errorf("failed to register agent type: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "✓ Registered agent type: %s\n", name)
	fmt.Fprintf(&sb, "  Description: %s\n", description)
	if len(tools) > 0 {
		fmt.Fprintf(&sb, "  Tools: %s\n", strings.Join(tools, ", "))
	} else {
		sb.WriteString("  Tools: (default for type)\n")
	}
	if prompt != "" {
		fmt.Fprintf(&sb, "  Prompt: %s...\n", truncate(prompt, 50))
	}

	return sb.String(), nil
}

func cleanToolList(raw string) []string {
	parts := strings.Split(raw, ",")
	tools := make([]string, 0, len(parts))
	for _, part := range parts {
		tool := strings.TrimSpace(part)
		if tool != "" {
			tools = append(tools, tool)
		}
	}
	return tools
}

// ListAgentTypesCommand lists all registered agent types.
type ListAgentTypesCommand struct{}

func (c *ListAgentTypesCommand) Name() string {
	return "list-agent-types"
}

func (c *ListAgentTypesCommand) Description() string {
	return "List all registered agent types (built-in and custom)"
}

func (c *ListAgentTypesCommand) Usage() string {
	return "/list-agent-types"
}

func (c *ListAgentTypesCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "list",
		Priority: 31,
	}
}

func (c *ListAgentTypesCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	registry := app.GetAgentTypeRegistry()
	if registry == nil {
		return "", fmt.Errorf("agent type registry not available")
	}

	var sb strings.Builder

	// Built-in types
	sb.WriteString("**Built-in Agent Types:**\n\n")
	builtinTypes := []struct {
		name string
		desc string
	}{
		{string(agent.AgentTypeExplore), registry.GetDescriptionForType(string(agent.AgentTypeExplore))},
		{string(agent.AgentTypeBash), registry.GetDescriptionForType(string(agent.AgentTypeBash))},
		{string(agent.AgentTypeGeneral), registry.GetDescriptionForType(string(agent.AgentTypeGeneral))},
		{string(agent.AgentTypePlan), registry.GetDescriptionForType(string(agent.AgentTypePlan))},
		{string(agent.AgentTypeGuide), registry.GetDescriptionForType(string(agent.AgentTypeGuide))},
	}

	for _, t := range builtinTypes {
		fmt.Fprintf(&sb, "• **%s** — %s\n", t.name, t.desc)
	}

	// Dynamic types
	dynamicTypes := registry.ListDynamic()
	if len(dynamicTypes) > 0 {
		sb.WriteString("\n**Custom Agent Types:**\n\n")
		for _, dt := range dynamicTypes {
			source := dt.Source
			if source == "" {
				source = "runtime"
			}
			fmt.Fprintf(&sb, "• **%s** [%s] — %s\n", dt.Name, source, dt.Description)
			if len(dt.AllowedTools) > 0 {
				fmt.Fprintf(&sb, "  Tools: %s\n", strings.Join(dt.AllowedTools, ", "))
			}
		}
	} else {
		sb.WriteString("\n*No custom agent types registered.*\n")
		sb.WriteString("\nUse `/register-agent-type` to add custom types.\n")
	}

	return sb.String(), nil
}

// UnregisterAgentTypeCommand removes a custom agent type.
type UnregisterAgentTypeCommand struct{}

func (c *UnregisterAgentTypeCommand) Name() string {
	return "unregister-agent-type"
}

func (c *UnregisterAgentTypeCommand) Description() string {
	return "Remove a custom agent type"
}

func (c *UnregisterAgentTypeCommand) Usage() string {
	return "/unregister-agent-type <name>"
}

func (c *UnregisterAgentTypeCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryTools,
		Icon:     "trash",
		HasArgs:  true,
		ArgHint:  "<name>",
		Priority: 32,
		Advanced: true,
	}
}

func (c *UnregisterAgentTypeCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if len(args) < 1 {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}

	registry := app.GetAgentTypeRegistry()
	if registry == nil {
		return "", fmt.Errorf("agent type registry not available")
	}

	name := args[0]

	// Check if it's a built-in type
	if registry.IsBuiltin(name) {
		return "", fmt.Errorf("cannot unregister built-in agent type: %s", name)
	}

	// Check if it exists
	if !registry.IsDynamic(name) {
		return "", fmt.Errorf("custom agent type not found: %s", name)
	}

	if err := registry.UnregisterDynamic(name); err != nil {
		return "", fmt.Errorf("failed to unregister agent type: %w", err)
	}

	return fmt.Sprintf("✓ Unregistered agent type: %s", name), nil
}

// truncate truncates a string to maxLen characters with ellipsis.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 {
		return ""
	}
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
