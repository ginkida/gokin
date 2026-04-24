package commands

import (
	"context"
)

// PlanCommand toggles planning mode.
type PlanCommand struct{}

func (c *PlanCommand) Name() string {
	return "plan"
}

func (c *PlanCommand) Description() string {
	return "Toggle planning mode for complex multi-step tasks"
}

func (c *PlanCommand) Usage() string {
	return "/plan"
}

func (c *PlanCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	// Show current state without toggling if "status" arg given
	if len(args) > 0 && args[0] == "status" {
		if app.IsPlanningModeEnabled() {
			msg := "Planning mode: ON"
			if pm := app.GetPlanManager(); pm != nil {
				if plan := pm.GetCurrentPlan(); plan != nil && !plan.IsComplete() {
					msg += "\nActive plan: " + plan.Title
				}
			}
			return msg, nil
		}
		return "Planning mode: OFF", nil
	}

	// Toggle planning mode
	enabled := app.TogglePlanningMode()

	if enabled {
		return "Plan mode ON — read-only exploration.\n\n" +
			"The agent can now only read/search/grep. When it has a plan, it will\n" +
			"call enter_plan_mode and ask for your approval. On approve, plan mode\n" +
			"exits automatically and it runs the plan with full tools.\n\n" +
			"Tip: Shift+Tab cycles modes (Normal → Plan → YOLO → Normal).\n" +
			"  · Plan: this mode — agent proposes plans, you approve before execution.\n" +
			"  · YOLO: permissions + sandbox OFF, agent runs everything without asking.\n" +
			"  · Normal: agent asks before write/edit/bash.", nil
	}
	return "Plan mode OFF — direct execution with full tools.\n\n" +
		"Tip: Shift+Tab cycles modes (Normal → Plan → YOLO → Normal).", nil
}

// GetMetadata returns command metadata for palette display.
func (c *PlanCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "tree",
		ArgHint:  "",
		Priority: 0, // Top of planning category
	}
}
