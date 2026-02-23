package commands

import (
	"context"
	"fmt"
)

// UndoCommand reverts the last file change.
type UndoCommand struct{}

func (c *UndoCommand) Name() string        { return "undo" }
func (c *UndoCommand) Description() string { return "Undo last file change" }
func (c *UndoCommand) Usage() string       { return "/undo" }
func (c *UndoCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "undo",
		Priority: 70,
	}
}

func (c *UndoCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetUndoManager()
	if mgr == nil {
		return "Undo manager not available.", nil
	}

	change, err := mgr.Undo()
	if err != nil {
		return fmt.Sprintf("Undo: %s", err.Error()), nil
	}

	return fmt.Sprintf("Undone: %s", change.Summary()), nil
}

// RedoCommand re-applies the last undone change.
type RedoCommand struct{}

func (c *RedoCommand) Name() string        { return "redo" }
func (c *RedoCommand) Description() string { return "Redo last undone change" }
func (c *RedoCommand) Usage() string       { return "/redo" }
func (c *RedoCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "redo",
		Priority: 71,
	}
}

func (c *RedoCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetUndoManager()
	if mgr == nil {
		return "Undo manager not available.", nil
	}

	change, err := mgr.Redo()
	if err != nil {
		return fmt.Sprintf("Redo: %s", err.Error()), nil
	}

	return fmt.Sprintf("Redone: %s", change.Summary()), nil
}

// CostCommand shows token usage (alias for /stats).
type CostCommand struct{}

func (c *CostCommand) Name() string        { return "cost" }
func (c *CostCommand) Description() string { return "Show token usage and cost" }
func (c *CostCommand) Usage() string       { return "/cost" }
func (c *CostCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "stats",
		Priority: 61,
	}
}

func (c *CostCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	stats := app.GetTokenStats()
	return fmt.Sprintf("Token usage:\n  Input:  %d\n  Output: %d\n  Total:  %d\n\nUse /stats for detailed session statistics.",
		stats.InputTokens, stats.OutputTokens, stats.TotalTokens), nil
}
