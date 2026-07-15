package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/client"
	"gokin/internal/config"
)

// ModelCommand switches the current model.
type ModelCommand struct{}

func (c *ModelCommand) Name() string        { return "model" }
func (c *ModelCommand) Description() string { return "Switch AI model" }
func (c *ModelCommand) Usage() string {
	return `/model                  - Show current model and available models
/model glm-5.2          - GLM-5.2 (default, 1M context, 131K output)
/model kimi-for-coding  - Kimi K2.6 Coding Plan (262K context)
/model glm-5.1          - GLM-5.1 (previous flagship)
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

	// No args - show current model and available models for this provider
	if len(args) == 0 {
		if len(providerModels) == 0 {
			return fmt.Sprintf("No models available for provider: %s", activeProvider), nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Provider: %s\n", activeProvider)
		fmt.Fprintf(&sb, "Model:    %s\n\n", currentModel)
		sb.WriteString("Available models:\n")

		for _, m := range providerModels {
			marker := "  "
			if m.ID == currentModel {
				marker = "> "
			}
			shortName := extractShortName(m.ID)
			fmt.Fprintf(&sb, "%s%-8s %s\n", marker, shortName, m.Description)
		}

		sb.WriteString("\nUsage: /model <name>")
		switch activeProvider {
		case "glm":
			sb.WriteString("\nExamples: /model glm-5.2  or  /model glm-5.1  or  /model glm-4.7")
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

	matchedProvider := activeProvider
	matchedModel, ambiguous := matchModelInProvider(newModel, providerModels)
	if ambiguous {
		return fmt.Sprintf("Ambiguous model name '%s'. Please be more specific.", newModel), nil
	}
	if matchedModel == "" {
		var crossProviderAmbiguous bool
		matchedProvider, matchedModel, crossProviderAmbiguous = matchModelAcrossProviders(newModel, activeProvider)
		if crossProviderAmbiguous {
			return fmt.Sprintf("Ambiguous model name '%s'. Please be more specific.", newModel), nil
		}
	}

	if matchedModel == "" {
		return fmt.Sprintf("Unknown model: %s\n\n%s", newModel, c.formatProviderModels(providerModels)), nil
	}

	if matchedProvider == activeProvider && matchedModel == currentModel {
		return fmt.Sprintf("Already using %s", currentModel), nil
	}

	if matchedProvider != activeProvider {
		p := config.GetProvider(matchedProvider)
		if p == nil {
			return fmt.Sprintf("Unknown provider for model: %s", matchedProvider), nil
		}
		if p.KeyOptional && p.HasOAuth && !cfg.API.HasOAuthToken(matchedProvider) {
			return fmt.Sprintf("%s requires OAuth login.\n\nUse: /oauth-login %s", p.DisplayName, matchedProvider), nil
		}
		if !p.KeyOptional && !cfg.API.HasProvider(matchedProvider) {
			return fmt.Sprintf("%s is not configured.\n\nUse: /login %s <api_key>", p.DisplayName, matchedProvider), nil
		}
	}

	// Stage the full model/provider selection first. ApplyConfig constructs and
	// installs the replacement client only after the snapshot commits, so a
	// conflict or client-build failure cannot mutate the live session early.
	cfg.API.ActiveProvider = matchedProvider
	cfg.Model.Provider = matchedProvider
	cfg.Model.Name = matchedModel
	// Clear any stale model.preset: an explicit model choice must win.
	// Otherwise MigrateConfig/NormalizeConfig (run on every ApplyConfig via
	// NewClient) re-apply the preset and silently revert this selection.
	cfg.Model.Preset = ""

	if preset, ok := config.ModelPresets[matchedProvider]; ok {
		cfg.Model.MaxOutputTokens = preset.MaxOutputTokens
	}

	if err := app.ApplyConfig(cfg); err != nil {
		return fmt.Sprintf("Failed to save: %v", err), nil
	}

	if matchedProvider != activeProvider {
		app.ClearConversation()
	}

	// Find model info for nice output
	var modelName string
	for _, m := range client.GetModelsForProvider(matchedProvider) {
		if m.ID == matchedModel {
			modelName = m.Name
			break
		}
	}

	if matchedProvider != activeProvider {
		return fmt.Sprintf("Switched to %s (%s) on %s — session cleared", modelName, matchedModel, matchedProvider), nil
	}
	return fmt.Sprintf("Switched to %s (%s)", modelName, matchedModel), nil
}

func matchModelInProvider(query string, models []client.ModelInfo) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(query))
	if normalized == "" {
		return "", false
	}

	for _, m := range models {
		if strings.ToLower(m.ID) == normalized ||
			strings.ToLower(extractShortName(m.ID)) == normalized {
			return m.ID, false
		}
	}

	var matched string
	for _, m := range models {
		modelID := strings.ToLower(m.ID)
		shortName := strings.ToLower(extractShortName(m.ID))
		if strings.Contains(modelID, normalized) || strings.Contains(shortName, normalized) {
			if matched != "" {
				return "", true
			}
			matched = m.ID
		}
	}
	return matched, false
}

func matchModelAcrossProviders(query, activeProvider string) (string, string, bool) {
	type match struct {
		provider string
		model    string
	}
	var matches []match

	for _, provider := range config.ProviderNames() {
		if provider == activeProvider {
			continue
		}
		if modelID, ambiguous := matchModelInProvider(query, client.GetModelsForProvider(provider)); ambiguous {
			return "", "", true
		} else if modelID != "" {
			matches = append(matches, match{provider: provider, model: modelID})
		}
	}

	if len(matches) == 0 {
		return "", "", false
	}
	if len(matches) > 1 {
		return "", "", true
	}
	return matches[0].provider, matches[0].model, false
}

func (c *ModelCommand) formatProviderModels(models []client.ModelInfo) string {
	var sb strings.Builder
	sb.WriteString("Available models:\n")
	for _, m := range models {
		shortName := extractShortName(m.ID)
		fmt.Fprintf(&sb, "  %-8s %s\n", shortName, m.Description)
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
	if after, ok := strings.CutPrefix(modelID, "MiniMax-"); ok {
		return after
	}
	if after, ok := strings.CutPrefix(modelID, "minimax-"); ok {
		return after
	}

	// Kimi models
	if after, ok := strings.CutPrefix(modelID, "kimi-"); ok {
		return after
	}

	// DeepSeek models — strip the provider prefix so /model v4-pro works.
	if after, ok := strings.CutPrefix(modelID, "deepseek-"); ok {
		return after
	}

	// Ollama and unknown — return full id
	return modelID
}
