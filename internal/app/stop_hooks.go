package app

import (
	"context"
	"errors"

	"gokin/internal/client"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

func terminalInternalExchangeError(err error) bool {
	if failure, ok := client.OutputRecoveryError(err); ok && failure.Exhausted {
		return true
	}
	return errors.Is(err, tools.ErrStrictPermissionDenied) || tools.IsHookStopError(err) || errors.Is(err, tools.ErrMaxTurnsExceeded) ||
		errors.Is(err, tools.ErrBudgetExceeded) || errors.Is(err, tools.ErrCostUnavailable)
}

// A Stop continuation is part of the invocation that already performed work.
// Preserve its paired history/checkpoints and return a terminal verdict rather
// than scheduling a new top-level provider retry or background recovery.
func (a *App) reportHeadlessStopFailure(ctx context.Context, turn *headlessTerminalOutcome, err error) {
	a.syncToolCheckpoints()
	if ctx.Err() != nil {
		return // RunHeadless reports the owning cancellation/deadline.
	}
	kind := "execution"
	_, degenerate := client.OutputRecoveryError(err)
	switch {
	case degenerate:
		kind = "degenerate_output"
	case errors.Is(err, tools.ErrMaxTurnsExceeded):
		kind = "max_turns"
	case errors.Is(err, tools.ErrBudgetExceeded):
		kind = "budget_exceeded"
	case errors.Is(err, tools.ErrCostUnavailable):
		kind = "cost_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		kind = "timeout"
	case tools.IsHookStopError(err):
		kind = "hook_stopped"
	case isHeadlessTimeoutFailure(err):
		kind = "timeout"
	}
	a.recordHeadlessTerminalOutcomeForTurn(turn, kind, err.Error())
	a.currentPresenter().Warning(err.Error())
	a.safeSendToProgram(ui.ErrorMsg(err))
}
