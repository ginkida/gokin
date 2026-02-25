package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/client"
)

// ModelCommand switches the current model.
type ModelCommand struct{}

func (c *ModelCommand) Name() string        { return "model" }
func (c *ModelCommand) Description() string { return "Switch AI model" }
func (c *ModelCommand) Usage() string {
	return `/model           - Show current model and available models
/model 3.1-pro   - Gemini 3.1 Pro (top reasoning)
/model 3-flash   - Gemini 3 Flash (fast & cheap)
/model 3-pro     - Gemini 3 Pro (advanced)
/model 5.3-codex-spark - GPT-5.3 Codex Spark (near-instant)
/model 5.3-codex       - GPT-5.3 Codex (most capable)
/model 5.2-codex       - GPT-5.2 Codex (400K context)
/model sonnet    - Claude Sonnet 4.5
/model opus      - Claude Opus 4.6`
}
func (c *ModelCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategorySession,
		Icon:        "model",
		Priority:    0,
		RequiresAPI: true,
		HasArgs:     true,
		ArgHint:     "name",
	}
}

func (c *ModelCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Failed to get configuration.", nil
	}

	setter := app.GetModelSetter()
	if setter == nil {
		return "Model switching not available.", nil
	}

	currentModel := setter.GetModel()
	activeProvider := cfg.API.GetActiveProvider()

	// Get models for current provider
	providerModels := client.GetModelsForProvider(activeProvider)
	if len(providerModels) == 0 {
		return fmt.Sprintf("No models available for provider: %s", activeProvider), nil
	}

	// No args - show current model and available models for this provider
	if len(args) == 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Provider: %s\n", activeProvider))
		sb.WriteString(fmt.Sprintf("Model:    %s\n\n", currentModel))
		sb.WriteString("Available models:\n")

		for _, m := range providerModels {
			marker := "  "
			if m.ID == currentModel {
				marker = "> "
			}
			shortName := extractShortName(m.ID)
			sb.WriteString(fmt.Sprintf("%s%-8s %s\n", marker, shortName, m.Description))
		}

		sb.WriteString("\nUsage: /model <name>")
		switch activeProvider {
		case "gemini":
			sb.WriteString("\nExamples: /model 3.1-pro  or  /model 3-flash  or  /model 2.5-pro")
		case "anthropic":
			sb.WriteString("\nExamples: /model sonnet  or  /model opus")
		case "openai":
			sb.WriteString("\nExamples: /model 5.3-codex  or  /model 5.3-codex-spark  or  /model 5.2-codex")
		case "minimax":
			sb.WriteString("\nExamples: /model M2.5  or  /model M2.5-highspeed")
		case "kimi":
			sb.WriteString("\nExamples: /model k2.5  or  /model k2-thinking-turbo")
		case "deepseek":
			sb.WriteString("\nExamples: /model chat  or  /model reasoner")
		}
		sb.WriteString("\n\nUse /provider to switch providers")

		return sb.String(), nil
	}

	// Switch to specified model
	newModel := args[0]

	// Find matching model within current provider
	var matchedModel string
	for _, m := range providerModels {
		if m.ID == newModel {
			matchedModel = m.ID
			break
		}
		// Partial match (e.g., "flash" matches "gemini-3-flash-preview")
		if strings.Contains(m.ID, newModel) || strings.Contains(extractShortName(m.ID), newModel) {
			if matchedModel != "" {
				return fmt.Sprintf("Ambiguous model name '%s'. Please be more specific.", newModel), nil
			}
			matchedModel = m.ID
		}
	}

	if matchedModel == "" {
		return fmt.Sprintf("Unknown model: %s\n\n%s", newModel, c.formatProviderModels(providerModels)), nil
	}

	if matchedModel == currentModel {
		return fmt.Sprintf("Already using %s", currentModel), nil
	}

	// Update model in config and setter
	setter.SetModel(matchedModel)
	cfg.Model.Name = matchedModel

	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}

	// Find model info for nice output
	var modelName string
	for _, m := range providerModels {
		if m.ID == matchedModel {
			modelName = m.Name
			break
		}
	}

	return fmt.Sprintf("Switched to %s (%s)", modelName, matchedModel), nil
}

func (c *ModelCommand) formatProviderModels(models []client.ModelInfo) string {
	var sb strings.Builder
	sb.WriteString("Available models:\n")
	for _, m := range models {
		shortName := extractShortName(m.ID)
		sb.WriteString(fmt.Sprintf("  %-8s %s\n", shortName, m.Description))
	}
	return sb.String()
}

// extractShortName extracts a short name from model ID
func extractShortName(modelID string) string {
	// Claude models
	if strings.HasPrefix(modelID, "claude") {
		if strings.Contains(modelID, "opus") {
			return "opus"
		}
		if strings.Contains(modelID, "sonnet") {
			return "sonnet"
		}
		if strings.Contains(modelID, "haiku") {
			return "haiku"
		}
		return modelID
	}

	// GLM models
	if strings.HasPrefix(modelID, "glm") {
		return modelID // Return full ID for GLM (e.g., "glm-4.7")
	}

	// OpenAI models: gpt-5.3-codex → 5.3-codex, gpt-5.2 → 5.2
	if strings.HasPrefix(modelID, "gpt-") {
		return strings.TrimPrefix(modelID, "gpt-")
	}

	// MiniMax models
	if strings.HasPrefix(modelID, "MiniMax-") || strings.HasPrefix(modelID, "minimax-") {
		short := strings.TrimPrefix(modelID, "MiniMax-")
		short = strings.TrimPrefix(short, "minimax-")
		return short
	}

	// Kimi models
	if strings.HasPrefix(modelID, "kimi-") {
		return strings.TrimPrefix(modelID, "kimi-")
	}

	// DeepSeek models
	if strings.HasPrefix(modelID, "deepseek-") {
		return strings.TrimPrefix(modelID, "deepseek-")
	}

	// Gemini 3.1 models (check before 3 to avoid collision)
	if strings.Contains(modelID, "gemini-3.1") {
		if strings.Contains(modelID, "pro") {
			return "3.1-pro"
		}
	}

	// Gemini 3 models
	if strings.Contains(modelID, "gemini-3") {
		if strings.Contains(modelID, "flash") {
			return "3-flash"
		}
		if strings.Contains(modelID, "pro") {
			return "3-pro"
		}
	}

	// Gemini 2.5 models
	if strings.Contains(modelID, "gemini-2.5") {
		if strings.Contains(modelID, "flash") {
			return "2.5-flash"
		}
		if strings.Contains(modelID, "pro") {
			return "2.5-pro"
		}
	}

	// Generic fallback
	if strings.Contains(modelID, "flash") {
		return "flash"
	}
	if strings.Contains(modelID, "pro") {
		return "pro"
	}

	return modelID
}
