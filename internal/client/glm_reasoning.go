package client

import (
	"strings"

	"gokin/internal/config"
)

// applyReasoningSettings is shared by prompt and tool-result requests so an
// adaptive budget change cannot disable GLM-5.3 in the middle of a coding loop.
func (c *AnthropicClient) applyReasoningSettings(body map[string]any, model string, enabled bool, budget int32, effort string, maxTokens int32, temperature float32) {
	if c.isProvider("glm") && config.GLMAlwaysOnReasoning(model) {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "" || effort == "auto" {
			if !enabled {
				budget = 0
			}
			effort = config.GLMEffortForBudget(budget)
		}
		// Z.AI's Anthropic gateway uses output_config.effort. Numeric Anthropic
		// budgets do not select a GLM effort level. Keep unsigned reasoning for
		// tool continuation and prefix-cache continuity, as with GLM-5.2.
		body["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
		body["output_config"] = map[string]any{"effort": effort}
		body["temperature"] = 1.0
		return
	}
	if enabled && budget > 0 {
		thinking := map[string]any{
			"type":          "enabled",
			"budget_tokens": clampThinkingBudgetBelowMax(budget, maxTokens),
		}
		if c.isProvider("glm") {
			thinking["clear_thinking"] = false
		}
		body["thinking"] = thinking
		body["temperature"] = 1.0
		return
	}
	if c.isProvider("kimi") && !kimiAlwaysOnReasoningModel(model) {
		body["thinking"] = map[string]any{"type": "disabled"}
	}
	if temperature > 0 {
		body["temperature"] = temperature
	}
}
