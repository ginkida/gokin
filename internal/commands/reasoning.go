package commands

import (
	"context"
	"fmt"
	"strings"
)

// Valid OpenAI reasoning effort levels.
var validReasoningEfforts = []string{"none", "low", "medium", "high", "xhigh"}

// ReasoningCommand changes the reasoning effort level (OpenAI and Gemini).
type ReasoningCommand struct{}

func (c *ReasoningCommand) Name() string        { return "reasoning" }
func (c *ReasoningCommand) Description() string { return "Set reasoning effort" }
func (c *ReasoningCommand) Usage() string {
	return `/reasoning          - Show current reasoning effort
/reasoning xhigh   - Maximum reasoning (default)
/reasoning high    - High reasoning
/reasoning medium  - Balanced reasoning
/reasoning low     - Minimal reasoning
/reasoning none    - Disable reasoning`
}
func (c *ReasoningCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "model",
		Priority: 5,
		HasArgs:  true,
		ArgHint:  "none|low|medium|high|xhigh",
	}
}

func (c *ReasoningCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	current := cfg.Model.ReasoningEffort
	if current == "" {
		current = "xhigh"
	}

	// No args — show current and available levels
	if len(args) == 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Provider: %s\n", cfg.API.GetActiveProvider()))
		sb.WriteString(fmt.Sprintf("Model:    %s\n\n", cfg.Model.Name))
		sb.WriteString("Reasoning effort levels:\n")

		for _, level := range validReasoningEfforts {
			marker := "  "
			if level == current {
				marker = "> "
			}
			sb.WriteString(fmt.Sprintf("%s%-8s %s\n", marker, level, effortDescription(level)))
		}

		sb.WriteString(fmt.Sprintf("\nCurrent: %s\n", current))
		sb.WriteString("Usage: /reasoning <level>")
		return sb.String(), nil
	}

	// Set new level
	newLevel := strings.ToLower(args[0])

	if !isValidEffort(newLevel) {
		return fmt.Sprintf("Unknown reasoning effort: %s\n\nValid levels: %s",
			newLevel, strings.Join(validReasoningEfforts, ", ")), nil
	}

	if newLevel == current {
		return fmt.Sprintf("Already using %s reasoning", current), nil
	}

	cfg.Model.ReasoningEffort = newLevel
	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}

	return fmt.Sprintf("Reasoning effort: %s → %s (%s)", current, newLevel, effortDescription(newLevel)), nil
}

func isValidEffort(level string) bool {
	for _, v := range validReasoningEfforts {
		if v == level {
			return true
		}
	}
	return false
}

func effortDescription(level string) string {
	switch level {
	case "none":
		return "Disabled — fastest, no chain-of-thought"
	case "low":
		return "Minimal reasoning"
	case "medium":
		return "Balanced speed and quality"
	case "high":
		return "Thorough reasoning"
	case "xhigh":
		return "Maximum reasoning (default)"
	default:
		return ""
	}
}
