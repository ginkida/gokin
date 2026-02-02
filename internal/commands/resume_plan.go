package commands

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/plan"
)

// ResumePlanCommand resumes a paused plan execution.
type ResumePlanCommand struct{}

func (c *ResumePlanCommand) Name() string        { return "resume-plan" }
func (c *ResumePlanCommand) Description() string { return "Resume a paused/failed plan execution" }
func (c *ResumePlanCommand) Usage() string {
	return `/resume-plan         - Resume most recent paused plan
/resume-plan list    - List all resumable plans
/resume-plan <id>    - Resume a specific plan by ID`
}

func (c *ResumePlanCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryPlanning,
		Icon:     "play",
		Priority: 10,
		HasArgs:  true,
		ArgHint:  "[list|<plan_id>]",
	}
}

func (c *ResumePlanCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	planMgr := app.GetPlanManager()
	if planMgr == nil {
		return "Plan manager not available.", nil
	}

	// Handle subcommands
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "list":
			return c.listResumablePlans(planMgr)
		default:
			// Treat as plan ID
			return c.resumePlanByID(planMgr, args[0])
		}
	}

	// Default: resume most recent plan

	// First check in-memory plan
	currentPlan := planMgr.GetCurrentPlan()
	if currentPlan != nil && currentPlan.PendingCount() > 0 {
		return c.resumePlan(planMgr, currentPlan)
	}

	// Try to load from storage
	loadedPlan, err := planMgr.LoadPausedPlan()
	if err != nil {
		// No plan in memory and nothing in storage
		return c.showNoPlanMessage(planMgr)
	}

	return c.resumePlan(planMgr, loadedPlan)
}

func (c *ResumePlanCommand) listResumablePlans(planMgr *plan.Manager) (string, error) {
	plans, err := planMgr.ListResumablePlans()
	if err != nil {
		return fmt.Sprintf("Failed to list plans: %v", err), nil
	}

	if len(plans) == 0 {
		return "No resumable plans found.", nil
	}

	var sb strings.Builder
	sb.WriteString("Resumable plans:\n\n")

	for _, p := range plans {
		statusIcon := "⏸"
		switch p.Status.String() {
		case "failed":
			statusIcon = "✗"
		case "in_progress":
			statusIcon = "◐"
		}

		sb.WriteString(fmt.Sprintf("%s %s\n", statusIcon, p.Title))
		sb.WriteString(fmt.Sprintf("   ID: %s\n", p.ID))
		sb.WriteString(fmt.Sprintf("   Progress: %d/%d steps (%.0f%%)\n", p.Completed, p.StepCount, p.Progress*100))
		sb.WriteString(fmt.Sprintf("   Updated: %s\n", p.UpdatedAt.Format("2006-01-02 15:04")))
		if p.Request != "" {
			sb.WriteString(fmt.Sprintf("   Request: %s\n", p.Request))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Use /resume-plan <id> to resume a specific plan.")

	return sb.String(), nil
}

func (c *ResumePlanCommand) resumePlanByID(planMgr *plan.Manager, planID string) (string, error) {
	// Check if it's the current plan
	currentPlan := planMgr.GetCurrentPlan()
	if currentPlan != nil && currentPlan.ID == planID {
		return c.resumePlan(planMgr, currentPlan)
	}

	// Load from storage
	loadedPlan, err := planMgr.LoadPlanByID(planID)
	if err != nil {
		return fmt.Sprintf("Failed to load plan '%s': %v\nUse /resume-plan list to see available plans.", planID, err), nil
	}

	return c.resumePlan(planMgr, loadedPlan)
}

func (c *ResumePlanCommand) resumePlan(planMgr *plan.Manager, p *plan.Plan) (string, error) {
	pendingCount := p.PendingCount()
	if pendingCount == 0 {
		return "Plan is already completed. No steps to resume.", nil
	}

	// Resume the plan
	resumedPlan, err := planMgr.ResumePlan()
	if err != nil {
		return fmt.Sprintf("Failed to resume plan: %v", err), nil
	}

	// Trigger plan execution via context-clear mechanism
	planMgr.RequestContextClear(resumedPlan)

	return fmt.Sprintf("Resuming plan: %s\n\n%d steps remaining. Executing...",
		resumedPlan.Title, pendingCount), nil
}

func (c *ResumePlanCommand) showNoPlanMessage(planMgr *plan.Manager) (string, error) {
	// Check if there are any saved plans
	plans, err := planMgr.ListResumablePlans()
	if err == nil && len(plans) > 0 {
		return fmt.Sprintf("No active plan in memory, but %d saved plan(s) found.\nUse /resume-plan list to see them.", len(plans)), nil
	}

	return "No resumable plan found.\n\nTo create a plan:\n1. Enable planning mode with /plan\n2. Describe a complex task\n3. The AI will create a step-by-step plan for approval", nil
}
