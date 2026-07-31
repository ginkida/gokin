package tools

import (
	"context"
	"fmt"

	"gokin/internal/plan"
	"gokin/internal/undo"

	"google.golang.org/genai"
)

// RedoPlanTool allows redoing a previously undone plan.
type RedoPlanTool struct {
	manager     *plan.Manager
	undoManager *undo.Manager
}

// NewRedoPlanTool creates a new redo plan tool.
func NewRedoPlanTool() *RedoPlanTool {
	return &RedoPlanTool{}
}

// SetManager sets the plan manager.
func (t *RedoPlanTool) SetManager(manager *plan.Manager) {
	t.manager = manager
}

// SetUndoManager sets the undo manager for redo operations.
func (t *RedoPlanTool) SetUndoManager(undoManager *undo.Manager) {
	t.undoManager = undoManager
}

func (t *RedoPlanTool) Name() string {
	return "redo_plan"
}

func (t *RedoPlanTool) Description() string {
	return "Safely re-apply only the tracked file changes from the latest undone plan"
}

func (t *RedoPlanTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"plan_id": {
					Type:        genai.TypeString,
					Description: "Optional: specific plan ID to redo (default: last undone plan)",
				},
			},
		},
	}
}

func (t *RedoPlanTool) Validate(args map[string]any) error {
	// No required parameters
	return nil
}

func (t *RedoPlanTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if t.manager == nil {
		return NewErrorResult("plan manager not configured"), nil
	}
	if t.undoManager == nil {
		return NewErrorResult("file undo manager not configured"), nil
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}

	if t.manager.IsExecuting() {
		return NewErrorResult("cannot redo plan during orchestrated execution — wait for the current plan to finish"), nil
	}

	undoExt := t.manager.GetUndoExtension()
	if undoExt == nil {
		return NewErrorResult("undo support is not enabled for this plan"), nil
	}
	if !undoExt.CanRedo() {
		return NewErrorResult("no plan execution history to redo"), nil
	}
	checkpoint := undoExt.GetLastCheckpoint()
	if checkpoint == nil {
		return NewErrorResult("no plan execution history to redo"), nil
	}
	if requestedPlanID, ok := GetString(args, "plan_id"); ok && requestedPlanID != "" {
		if requestedPlanID != checkpoint.PlanID {
			return NewErrorResult(fmt.Sprintf(
				"plan %s is not the latest safely redoable plan",
				requestedPlanID,
			)), nil
		}
	}

	changes, err := t.undoManager.RedoChanges(checkpoint.ChangeIDs)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("plan redo refused: %v", err)), nil
	}
	redoneFiles := make([]string, 0, len(changes))
	for _, change := range changes {
		redoneFiles = append(redoneFiles, change.FilePath)
	}
	if err := undoExt.MarkRedone(checkpoint.PlanID); err != nil {
		return NewErrorResult(fmt.Sprintf(
			"plan file changes were redone, but undo bookkeeping failed: %v",
			err,
		)), nil
	}

	// Build result message
	resultMsg := "Plan redone successfully\n"
	if len(redoneFiles) > 0 {
		resultMsg += fmt.Sprintf("\nRe-applied %d file changes:\n", len(redoneFiles))
		for _, file := range redoneFiles {
			resultMsg += fmt.Sprintf("  • %s\n", file)
		}
	}

	return NewSuccessResultWithData(
		resultMsg,
		map[string]any{
			"redone":       true,
			"plan_id":      checkpoint.PlanID,
			"plan_title":   checkpoint.PlanTitle,
			"redone_files": redoneFiles,
			// Mutates files outside the executor's write dispatch — declare the
			// paths so read-dedup and the result caches drop the stale content.
			"written_paths": redoneFiles,
		},
	), nil
}
