package tools

import (
	"context"
	"fmt"

	"gokin/internal/plan"
	"gokin/internal/undo"

	"google.golang.org/genai"
)

// UndoPlanTool allows undoing the last executed plan.
type UndoPlanTool struct {
	manager     *plan.Manager
	undoManager *undo.Manager
}

// NewUndoPlanTool creates a new undo plan tool.
func NewUndoPlanTool() *UndoPlanTool {
	return &UndoPlanTool{}
}

// SetManager sets the plan manager.
func (t *UndoPlanTool) SetManager(manager *plan.Manager) {
	t.manager = manager
}

// SetUndoManager sets the undo manager for file operations.
func (t *UndoPlanTool) SetUndoManager(undoManager *undo.Manager) {
	t.undoManager = undoManager
}

func (t *UndoPlanTool) Name() string {
	return "undo_plan"
}

func (t *UndoPlanTool) Description() string {
	return "Safely undo only the tracked file changes made by the latest executed plan"
}

func (t *UndoPlanTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"confirm": {
					Type:        genai.TypeString,
					Description: "Confirmation to undo the plan (must be 'yes' or 'true')",
				},
			},
		},
	}
}

func (t *UndoPlanTool) Validate(args map[string]any) error {
	confirm, ok := GetString(args, "confirm")
	if !ok || (confirm != "yes" && confirm != "true") {
		return NewValidationError("confirm", "confirmation must be 'yes' or 'true'")
	}
	return nil
}

func (t *UndoPlanTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
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
		return NewErrorResult("cannot undo plan during orchestrated execution — wait for the current plan to finish"), nil
	}

	undoExt := t.manager.GetUndoExtension()
	if undoExt == nil {
		return NewErrorResult("undo support is not enabled for this plan"), nil
	}

	checkpoint := undoExt.GetLastCheckpoint()
	if checkpoint == nil {
		return NewErrorResult("no plan execution history to undo"), nil
	}
	if !checkpoint.CaptureOK {
		reason := checkpoint.CaptureErr
		if reason == "" {
			reason = "the plan file-change boundary is incomplete"
		}
		return NewErrorResult(fmt.Sprintf("plan undo unavailable: %s", reason)), nil
	}
	if !undoExt.CanUndo() {
		return NewErrorResult("no plan execution history to undo"), nil
	}
	if len(checkpoint.ChangeIDs) == 0 {
		return NewErrorResult("the latest plan has no tracked file changes to undo"), nil
	}

	changes, err := t.undoManager.UndoChanges(checkpoint.ChangeIDs)
	if err != nil {
		return NewErrorResult(fmt.Sprintf("plan undo refused: %v", err)), nil
	}
	undoneFiles := make([]string, 0, len(changes))
	for _, change := range changes {
		undoneFiles = append(undoneFiles, change.FilePath)
	}
	if err := undoExt.MarkUndone(checkpoint.PlanID); err != nil {
		// The filesystem transaction succeeded, so expose the bookkeeping error
		// instead of falsely claiming a fully redoable plan undo.
		return NewErrorResult(fmt.Sprintf(
			"plan file changes were undone, but redo bookkeeping failed: %v",
			err,
		)), nil
	}

	// Build result message
	resultMsg := fmt.Sprintf("Plan file changes undone safely: %s\n", checkpoint.PlanTitle)
	if len(undoneFiles) > 0 {
		resultMsg += fmt.Sprintf("\nReverted %d file changes:\n", len(undoneFiles))
		for _, file := range undoneFiles {
			resultMsg += fmt.Sprintf("  • %s\n", file)
		}
	}
	if len(checkpoint.Executed) > 0 {
		resultMsg += fmt.Sprintf("\nExecuted steps that were undone: %v\n", checkpoint.Executed)
	}

	return NewSuccessResultWithData(
		resultMsg,
		map[string]any{
			"undone":         true,
			"plan_id":        checkpoint.PlanID,
			"plan_title":     checkpoint.PlanTitle,
			"executed_steps": checkpoint.Executed,
			"undone_files":   undoneFiles,
			// This tool rewrites files through the undo manager, outside the
			// executor's write dispatch, so it must declare the paths itself —
			// otherwise the read-dedup tracker and the result caches keep
			// serving the pre-undo content.
			"written_paths": undoneFiles,
		},
	), nil
}
