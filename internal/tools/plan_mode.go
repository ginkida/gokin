package tools

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/plan"

	"google.golang.org/genai"
)

// EnterPlanModeTool allows the model to create execution plans and request user approval.
type EnterPlanModeTool struct {
	manager *plan.Manager
}

// NewEnterPlanModeTool creates a new enter plan mode tool.
func NewEnterPlanModeTool() *EnterPlanModeTool {
	return &EnterPlanModeTool{}
}

// SetManager sets the plan manager.
func (t *EnterPlanModeTool) SetManager(manager *plan.Manager) {
	t.manager = manager
}

func (t *EnterPlanModeTool) Name() string {
	return "enter_plan_mode"
}

func (t *EnterPlanModeTool) Description() string {
	return "Create an execution plan and request user approval before proceeding with implementation"
}

// StepInput represents a step in the plan.
type StepInput struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

func (t *EnterPlanModeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"title": {
					Type:        genai.TypeString,
					Description: "Title of the plan (short description of what will be done)",
				},
				"description": {
					Type:        genai.TypeString,
					Description: "Detailed description of the plan",
				},
				"steps": {
					Type:        genai.TypeArray,
					Description: "List of steps in the plan",
					Items: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"title": {
								Type:        genai.TypeString,
								Description: "Short title for the step",
							},
							"description": {
								Type:        genai.TypeString,
								Description: "Detailed description of what the step involves",
							},
						},
						Required: []string{"title"},
					},
				},
				"request": {
					Type:        genai.TypeString,
					Description: "The original user request that prompted this plan",
				},
			},
			Required: []string{"title", "steps"},
		},
	}
}

func (t *EnterPlanModeTool) Validate(args map[string]any) error {
	if _, ok := GetString(args, "title"); !ok {
		return NewValidationError("title", "title is required")
	}

	stepsRaw, ok := args["steps"]
	if !ok {
		return NewValidationError("steps", "steps is required")
	}

	stepsSlice, ok := stepsRaw.([]any)
	if !ok || len(stepsSlice) == 0 {
		return NewValidationError("steps", "steps must be a non-empty array")
	}

	// Validate each step has a title
	for i, stepRaw := range stepsSlice {
		step, ok := stepRaw.(map[string]any)
		if !ok {
			return NewValidationError("steps", fmt.Sprintf("step %d is invalid", i))
		}
		if _, ok := step["title"].(string); !ok {
			return NewValidationError("steps", fmt.Sprintf("step %d must have a title", i))
		}
	}

	return nil
}

func (t *EnterPlanModeTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.manager == nil {
		return NewErrorResult("plan manager not configured"), nil
	}

	if !t.manager.IsEnabled() {
		return NewErrorResult("plan mode is disabled in configuration"), nil
	}

	// Check if we're currently executing an approved plan (nested plans not allowed)
	if t.manager.IsExecuting() {
		currentPlan := t.manager.GetCurrentPlan()
		stepID := t.manager.GetCurrentStepID()
		var stepInfo string
		if stepID >= 0 && currentPlan != nil {
			if step := currentPlan.GetStep(stepID); step != nil {
				stepInfo = fmt.Sprintf(" (currently executing step %d: %s)", stepID+1, step.Title)
			}
		}
		return NewErrorResult(fmt.Sprintf(
			"cannot create a new plan while executing an approved plan%s. Complete the current plan first or use update_plan_progress to report progress.",
			stepInfo,
		)), nil
	}

	// Check if there's already an active plan (in creation phase)
	if t.manager.IsActive() {
		currentPlan := t.manager.GetCurrentPlan()
		return NewErrorResult(fmt.Sprintf(
			"there is already an active plan: %s (progress: %.0f%%)",
			currentPlan.Title,
			currentPlan.Progress()*100,
		)), nil
	}

	// Extract parameters
	title, _ := GetString(args, "title")
	description, _ := GetString(args, "description")
	request, _ := GetString(args, "request")
	stepsRaw, _ := args["steps"]

	// Create the plan
	p := t.manager.CreatePlan(title, description, request)

	// Add steps
	if stepsSlice, ok := stepsRaw.([]any); ok {
		for _, stepRaw := range stepsSlice {
			if step, ok := stepRaw.(map[string]any); ok {
				stepTitle, _ := step["title"].(string)
				stepDesc, _ := step["description"].(string)
				p.AddStep(stepTitle, stepDesc)
			}
		}
	}

	// Request approval
	decision, err := t.manager.RequestApproval(ctx)
	if err != nil {
		t.manager.ClearPlan()
		return NewErrorResult(fmt.Sprintf("approval request failed: %s", err)), nil
	}

	// Handle decision
	switch decision {
	case plan.ApprovalApproved:
		// Signal context clear for focused plan execution
		t.manager.RequestContextClear(p)
		return NewSuccessResultWithData(
			fmt.Sprintf("Plan approved: %s\nContext will be cleared for focused execution of %d steps.",
				p.Title, p.StepCount()),
			map[string]any{
				"approved":              true,
				"plan_id":               p.ID,
				"step_count":            p.StepCount(),
				"decision":              "approved",
				"context_clear_pending": true,
			},
		), nil

	case plan.ApprovalRejected:
		t.manager.ClearPlan()
		return NewSuccessResultWithData(
			"Plan rejected by user. Please ask for clarification or propose a different approach.",
			map[string]any{
				"approved": false,
				"decision": "rejected",
			},
		), nil

	case plan.ApprovalModified:
		// User requested modifications
		return NewSuccessResultWithData(
			"User requested modifications to the plan. Please revise and resubmit.",
			map[string]any{
				"approved": false,
				"decision": "modification_requested",
			},
		), nil

	default:
		t.manager.ClearPlan()
		return NewErrorResult("unknown approval decision"), nil
	}
}

// UpdatePlanProgressTool updates the progress of plan execution.
type UpdatePlanProgressTool struct {
	manager *plan.Manager
}

// NewUpdatePlanProgressTool creates a new update plan progress tool.
func NewUpdatePlanProgressTool() *UpdatePlanProgressTool {
	return &UpdatePlanProgressTool{}
}

// SetManager sets the plan manager.
func (t *UpdatePlanProgressTool) SetManager(manager *plan.Manager) {
	t.manager = manager
}

func (t *UpdatePlanProgressTool) Name() string {
	return "update_plan_progress"
}

func (t *UpdatePlanProgressTool) Description() string {
	return "Update the progress of plan execution by marking steps as started, completed, failed, or skipped"
}

func (t *UpdatePlanProgressTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"step_id": {
					Type:        genai.TypeInteger,
					Description: "The ID of the step to update (1-indexed)",
				},
				"action": {
					Type:        genai.TypeString,
					Description: "Action to perform on the step",
					Enum:        []string{"start", "complete", "fail", "skip"},
				},
				"output": {
					Type:        genai.TypeString,
					Description: "Output or result message for the step (used with 'complete' or 'fail')",
				},
			},
			Required: []string{"step_id", "action"},
		},
	}
}

func (t *UpdatePlanProgressTool) Validate(args map[string]any) error {
	if _, ok := GetInt(args, "step_id"); !ok {
		return NewValidationError("step_id", "step_id is required")
	}

	action, ok := GetString(args, "action")
	if !ok {
		return NewValidationError("action", "action is required")
	}

	validActions := map[string]bool{
		"start": true, "complete": true, "fail": true, "skip": true,
	}
	if !validActions[action] {
		return NewValidationError("action", "action must be one of: start, complete, fail, skip")
	}

	return nil
}

func (t *UpdatePlanProgressTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.manager == nil {
		return NewErrorResult("plan manager not configured"), nil
	}

	if !t.manager.IsActive() {
		return NewErrorResult("no active plan"), nil
	}

	stepID, _ := GetInt(args, "step_id")
	action, _ := GetString(args, "action")
	output, _ := GetString(args, "output")

	p := t.manager.GetCurrentPlan()
	step := p.GetStep(stepID)
	if step == nil {
		return NewErrorResult(fmt.Sprintf("step %d not found", stepID)), nil
	}

	switch action {
	case "start":
		t.manager.StartStep(stepID)
		return NewSuccessResultWithData(
			fmt.Sprintf("Started step %d: %s", stepID, step.Title),
			buildProgressData(p, step, "started"),
		), nil

	case "complete":
		t.manager.CompleteStep(stepID, output)
		return NewSuccessResultWithData(
			fmt.Sprintf("Completed step %d: %s", stepID, step.Title),
			buildProgressData(p, step, "completed"),
		), nil

	case "fail":
		t.manager.FailStep(stepID, output)
		return NewSuccessResultWithData(
			fmt.Sprintf("Step %d failed: %s\nError: %s", stepID, step.Title, output),
			buildProgressData(p, step, "failed"),
		), nil

	case "skip":
		t.manager.SkipStep(stepID)
		return NewSuccessResultWithData(
			fmt.Sprintf("Skipped step %d: %s", stepID, step.Title),
			buildProgressData(p, step, "skipped"),
		), nil

	default:
		return NewErrorResult("invalid action"), nil
	}
}

func buildProgressData(p *plan.Plan, step *plan.Step, status string) map[string]any {
	// Use thread-safe methods for all plan data access
	current, total, percent := p.CompletedCount(), p.StepCount(), p.Progress()

	// Determine next step using thread-safe method
	nextStep := p.NextStep()

	data := map[string]any{
		"step_id":     step.ID,
		"step_title":  step.Title,
		"step_status": status,
		"completed":   current,
		"total":       total,
		"progress":    percent * 100,
		"plan_done":   p.IsComplete(),
	}

	if nextStep != nil {
		data["next_step_id"] = nextStep.ID
		data["next_step_title"] = nextStep.Title
	}

	return data
}

// GetPlanStatusTool returns the current plan status.
type GetPlanStatusTool struct {
	manager *plan.Manager
}

// NewGetPlanStatusTool creates a new get plan status tool.
func NewGetPlanStatusTool() *GetPlanStatusTool {
	return &GetPlanStatusTool{}
}

// SetManager sets the plan manager.
func (t *GetPlanStatusTool) SetManager(manager *plan.Manager) {
	t.manager = manager
}

func (t *GetPlanStatusTool) Name() string {
	return "get_plan_status"
}

func (t *GetPlanStatusTool) Description() string {
	return "Get the current status of the active plan"
}

func (t *GetPlanStatusTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type:       genai.TypeObject,
			Properties: map[string]*genai.Schema{},
		},
	}
}

func (t *GetPlanStatusTool) Validate(args map[string]any) error {
	return nil
}

func (t *GetPlanStatusTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.manager == nil {
		return NewErrorResult("plan manager not configured"), nil
	}

	if !t.manager.IsActive() {
		return NewSuccessResultWithData("No active plan", map[string]any{
			"active": false,
		}), nil
	}

	p := t.manager.GetCurrentPlan()

	// Build step status list using thread-safe snapshot
	stepsSnapshot := p.GetStepsSnapshot()
	steps := make([]map[string]any, 0, len(stepsSnapshot))
	for _, step := range stepsSnapshot {
		steps = append(steps, map[string]any{
			"id":          step.ID,
			"title":       step.Title,
			"description": step.Description,
			"status":      step.Status.String(),
		})
	}

	var builder strings.Builder
	builder.WriteString(p.Format())

	data := map[string]any{
		"active":      true,
		"plan_id":     p.ID,
		"title":       p.Title,
		"description": p.Description,
		"status":      p.Status.String(),
		"steps":       steps,
		"completed":   p.CompletedCount(),
		"total":       p.StepCount(),
		"progress":    p.Progress() * 100,
	}

	return NewSuccessResultWithData(builder.String(), data), nil
}

// ExitPlanModeTool exits plan mode and clears the current plan.
type ExitPlanModeTool struct {
	manager *plan.Manager
}

// NewExitPlanModeTool creates a new exit plan mode tool.
func NewExitPlanModeTool() *ExitPlanModeTool {
	return &ExitPlanModeTool{}
}

// SetManager sets the plan manager.
func (t *ExitPlanModeTool) SetManager(manager *plan.Manager) {
	t.manager = manager
}

func (t *ExitPlanModeTool) Name() string {
	return "exit_plan_mode"
}

func (t *ExitPlanModeTool) Description() string {
	return "Exit plan mode and clear the current plan (use when plan is complete or should be abandoned)"
}

func (t *ExitPlanModeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"reason": {
					Type:        genai.TypeString,
					Description: "Reason for exiting plan mode",
					Enum:        []string{"completed", "abandoned", "error"},
				},
				"summary": {
					Type:        genai.TypeString,
					Description: "Summary of what was accomplished (optional)",
				},
			},
		},
	}
}

func (t *ExitPlanModeTool) Validate(args map[string]any) error {
	return nil
}

func (t *ExitPlanModeTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.manager == nil {
		return NewErrorResult("plan manager not configured"), nil
	}

	reason := GetStringDefault(args, "reason", "completed")
	summary, _ := GetString(args, "summary")

	p := t.manager.GetCurrentPlan()
	var planInfo map[string]any
	if p != nil {
		planInfo = map[string]any{
			"plan_id":   p.ID,
			"title":     p.Title,
			"completed": p.CompletedCount(),
			"total":     p.StepCount(),
			"progress":  p.Progress() * 100,
		}
	}

	t.manager.ClearPlan()

	msg := fmt.Sprintf("Exited plan mode (reason: %s)", reason)
	if summary != "" {
		msg += "\n" + summary
	}

	data := map[string]any{
		"reason":  reason,
		"summary": summary,
	}
	if planInfo != nil {
		data["plan"] = planInfo
	}

	return NewSuccessResultWithData(msg, data), nil
}

// getStringField extracts a string from a map, returning empty string if not found.
func getStringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
