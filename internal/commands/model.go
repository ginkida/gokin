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
	return `/model                  - Show current model and available models
/model kimi-for-coding  - Kimi K2.6 Coding Plan (default, 262K context)
/model glm-5.1          - GLM-5.1 (newest, 131K output)
/model glm-5            - GLM-5 (stable)
/model glm-4.7          - GLM-4.7 (thinking-enabled)
/model MiniMax-M2.7     - MiniMax M2.7 (200K context)
/model deepseek-v4-pro  - DeepSeek V4 Pro (Strong-tier, 1M context)
/model deepseek-v4-flash - DeepSeek V4 Flash (fast & cheap, 1M context)`
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
		case "glm":
			sb.WriteString("\nExamples: /model glm-5.1  or  /model glm-5  or  /model glm-4.7")
		case "minimax":
			sb.WriteString("\nExamples: /model M2.7  or  /model M2.7-highspeed  or  /model M2.5")
		case "kimi":
			// Kimi Coding Plan currently serves only kimi-for-coding (K2.6).
			// Old k2.5 / k2-thinking-turbo examples pointed at the retired
			// Moonshot Developer API and caused confusion — removed.
			sb.WriteString("\nExample: /model kimi-for-coding  (only model on Coding Plan — K2.6, 262K context)")
		case "deepseek":
			sb.WriteString("\nExamples: /model deepseek-v4-pro  (flagship, Strong-tier, 1M ctx)" +
				"\n          /model deepseek-v4-flash  (fast/cheap V4, 1M ctx)" +
				"\n          /model deepseek-reasoner  (legacy, deprecates 2026-07-24)")
		case "ollama":
			sb.WriteString("\nExample: /model llama3.2  or any model pulled locally via `ollama pull …`")
		}
		sb.WriteString("\n\nUse /provider to switch providers")

		return sb.String(), nil
	}

	// Switch to specified model
	newModel := args[0]

	// Find matching model within current provider
	var matchedModel string

	// First pass: exact match on ID or short name
	for _, m := range providerModels {
		if m.ID == newModel || extractShortName(m.ID) == newModel {
			matchedModel = m.ID
			break
		}
	}

	// Second pass: partial/substring match (only if no exact match)
	if matchedModel == "" {
		for _, m := range providerModels {
			if strings.Contains(m.ID, newModel) || strings.Contains(extractShortName(m.ID), newModel) {
				if matchedModel != "" {
					return fmt.Sprintf("Ambiguous model name '%s'. Please be more specific.", newModel), nil
				}
				matchedModel = m.ID
			}
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
	// GLM models
	if strings.HasPrefix(modelID, "glm") {
		return modelID // Return full ID for GLM (e.g., "glm-4.7")
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

	// DeepSeek models — strip the provider prefix so /model v4-pro works.
	if strings.HasPrefix(modelID, "deepseek-") {
		return strings.TrimPrefix(modelID, "deepseek-")
	}

	// Ollama and unknown — return full id
	return modelID
}
