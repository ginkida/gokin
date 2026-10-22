package app

import (
	"strings"

	"gokin/internal/config"
)

func (a *App) applyHeadlessThinking(message string) {
	if a.taskRouter == nil || a.client == nil || a.config == nil ||
		runtimeProviderForConfig(a.config) != "glm" || !config.GLMAlwaysOnReasoning(a.config.Model.Name) ||
		a.config.Model.ThinkingBudget > 0 {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(a.config.Model.ReasoningEffort))
	if effort != "" && effort != "auto" {
		return
	}
	budget := a.taskRouter.ThinkingBudgetFor(message)
	a.client.SetThinkingBudget(budget)
	a.journalEvent("thinking_policy", map[string]any{
		"mode": "auto", "budget": budget, "effort": config.GLMEffortForBudget(budget),
	})
}
