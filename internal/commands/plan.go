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
		return "Planning mode ON — complex tasks will be broken into steps with approval\n\nTip: Shift+Tab to toggle, /plan status to check", nil
	}
	return "Planning mode OFF — direct execution\n\nTip: Shift+Tab to toggle, /plan status to check", nil
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
