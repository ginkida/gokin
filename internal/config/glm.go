package config

import (
	"fmt"
	"strings"
)

const (
	GLMFlagshipModel = "glm-5.3"
	GLMFlashModel    = "glm-5.3-flash"
	DefaultGLMModel  = GLMFlashModel
)

// GLMAlwaysOnReasoning includes the 5.3 Flash variants and tagged gateway IDs.
// Older GLM models still support disabling reasoning.
func GLMAlwaysOnReasoning(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return m == GLMFlagshipModel || strings.HasPrefix(m, GLMFlagshipModel+"-") ||
		strings.HasPrefix(m, GLMFlagshipModel+"[") || strings.HasPrefix(m, GLMFlagshipModel+":")
}

// GLMEffortForBudget translates the router's token budgets to the provider's
// supported effort levels. Zero means lightweight reasoning, never disabled.
func GLMEffortForBudget(budget int32) string {
	if budget <= 2048 {
		return "low"
	}
	if budget <= 8192 {
		return "high"
	}
	return "max"
}

// ValidateReasoningEffort catches unsupported values before an API request.
// Empty/auto lets the router and sub-agent policies choose per request.
func ValidateReasoningEffort(effort string) error {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "auto", "low", "high", "max":
		return nil
	default:
		return fmt.Errorf("invalid model.reasoning_effort %q: expected auto, low, high, or max", effort)
	}
}
