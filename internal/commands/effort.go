package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/config"
)

type EffortCommand struct{}

func (c *EffortCommand) Name() string        { return "effort" }
func (c *EffortCommand) Description() string { return "Choose GLM reasoning effort" }
func (c *EffortCommand) Usage() string       { return "/effort [auto|low|high|max]" }
func (c *EffortCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{Category: CategoryTools, Icon: "brain", Priority: 26, HasArgs: true, ArgHint: "auto|low|high|max"}
}

func (c *EffortCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	cfg := app.GetConfig()
	if cfg == nil {
		return "Config not available", nil
	}
	if !config.GLMAlwaysOnReasoning(cfg.Model.Name) {
		return "Effort levels require GLM-5.3 or GLM-5.3-Flash. Use /thinking for this model.", nil
	}
	if len(args) > 1 {
		return c.Usage(), nil
	}
	if len(args) == 1 {
		effort := strings.ToLower(strings.TrimSpace(args[0]))
		if effort == "" || config.ValidateReasoningEffort(effort) != nil {
			return "Unknown effort. " + c.Usage(), nil
		}
		cfg.Model.ReasoningEffort = effort
		if err := app.ApplyConfig(cfg); err != nil {
			return fmt.Sprintf("Failed to save: %v", err), nil
		}
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Model.ReasoningEffort))
	if effort == "" || effort == "auto" {
		return "effort: auto — adapts low/high/max to each task; reasoning stays enabled. Use /effort max for deep coding work.", nil
	}
	return fmt.Sprintf("effort: %s — overrides adaptive thinking budgets; reasoning stays enabled (%s)", effort, cfg.Model.Name), nil
}
