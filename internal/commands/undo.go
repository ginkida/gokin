package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appcontext "gokin/internal/context"
)

// MaxUndoSteps caps how many changes /undo N will revert in one call. Prevents
// accidental massive rewinds ("/undo 999" when user meant "/undo list").
const MaxUndoSteps = 20

// UndoCommand reverts the last file change, or multiple changes with /undo N.
type UndoCommand struct{}

func (c *UndoCommand) Name() string        { return "undo" }
func (c *UndoCommand) Description() string { return "Undo last file change(s)" }
func (c *UndoCommand) Usage() string {
	return `/undo           - Undo last file change
/undo N         - Undo last N changes (max 20)
/undo list      - Show recent undoable changes`
}
func (c *UndoCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "undo",
		Priority: 70,
		HasArgs:  true,
		ArgHint:  "[N|list]",
	}
}

func (c *UndoCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetUndoManager()
	if mgr == nil {
		return "Undo manager not available.", nil
	}

	// /undo list — preview recent changes without reverting anything.
	if len(args) > 0 && strings.EqualFold(args[0], "list") {
		recent := mgr.ListRecent(10)
		if len(recent) == 0 {
			return "No changes to undo.", nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Recent undoable changes (most recent first, total %d):\n", mgr.Count())
		for i := len(recent) - 1; i >= 0; i-- {
			fmt.Fprintf(&sb, "  %2d. %s\n", len(recent)-i, recent[i].Summary())
		}
		sb.WriteString("\nUse /undo N to revert the last N changes.")
		return sb.String(), nil
	}

	// /undo N — multi-step rewind. Stops at the first error and reports what
	// was actually reverted so the user isn't left guessing the stack state.
	steps := 1
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return fmt.Sprintf("Invalid argument %q. Use /undo [N|list].", args[0]), nil
		}
		if n > MaxUndoSteps {
			return fmt.Sprintf("Max %d steps per /undo. Run again if you need more.", MaxUndoSteps), nil
		}
		steps = n
	}

	if steps == 1 {
		change, err := mgr.Undo()
		if err != nil {
			return fmt.Sprintf("Undo: %s", err.Error()), nil
		}
		return fmt.Sprintf("Undone: %s", change.Summary()), nil
	}

	var reverted []string
	for i := 0; i < steps; i++ {
		change, err := mgr.Undo()
		if err != nil {
			if len(reverted) == 0 {
				return fmt.Sprintf("Undo: %s", err.Error()), nil
			}
			break
		}
		reverted = append(reverted, change.Summary())
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Undone %d change(s):\n", len(reverted))
	for i, s := range reverted {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, s)
	}
	return sb.String(), nil
}

// RedoCommand re-applies previously undone changes.
type RedoCommand struct{}

func (c *RedoCommand) Name() string        { return "redo" }
func (c *RedoCommand) Description() string { return "Redo last undone change(s)" }
func (c *RedoCommand) Usage() string       { return "/redo [N]" }
func (c *RedoCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategorySession,
		Icon:     "redo",
		Priority: 71,
		HasArgs:  true,
		ArgHint:  "[N]",
	}
}

func (c *RedoCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	mgr := app.GetUndoManager()
	if mgr == nil {
		return "Undo manager not available.", nil
	}

	steps := 1
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return fmt.Sprintf("Invalid argument %q. Use /redo [N].", args[0]), nil
		}
		if n > MaxUndoSteps {
			return fmt.Sprintf("Max %d steps per /redo. Run again if you need more.", MaxUndoSteps), nil
		}
		steps = n
	}

	if steps == 1 {
		change, err := mgr.Redo()
		if err != nil {
			return fmt.Sprintf("Redo: %s", err.Error()), nil
		}
		return fmt.Sprintf("Redone: %s", change.Summary()), nil
	}

	var applied []string
	for i := 0; i < steps; i++ {
		change, err := mgr.Redo()
		if err != nil {
			if len(applied) == 0 {
				return fmt.Sprintf("Redo: %s", err.Error()), nil
			}
			break
		}
		applied = append(applied, change.Summary())
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Redone %d change(s):\n", len(applied))
	for i, s := range applied {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, s)
	}
	return sb.String(), nil
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

	var costStr string
	cm := app.GetContextManager()
	if cm != nil {
		tc := cm.GetTokenCounter()
		if tc != nil {
			cost := tc.CalculateCost(stats.InputTokens, stats.OutputTokens)
			costStr = fmt.Sprintf("\n  Est. Cost: %s", appcontext.FormatCost(cost))
		}
	}

	modelStr := ""
	if ms := app.GetModelSetter(); ms != nil {
		if m := ms.GetModel(); m != "" {
			modelStr = fmt.Sprintf("\n  Model:  %s", m)
		}
	}

	return fmt.Sprintf("Token usage:\n  Input:  %s\n  Output: %s\n  Total:  %s%s%s",
		formatTokens(stats.InputTokens), formatTokens(stats.OutputTokens),
		formatTokens(stats.TotalTokens), costStr, modelStr), nil
}

func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
