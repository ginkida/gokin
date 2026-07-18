package app

import (
	"fmt"
	"sync/atomic"

	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

// Budget auto-continuation (v0.100.104 field report): a hosted-provider turn
// that exhausts its per-turn tool budget finalizes honestly — but with the
// model's own todo list still carrying unfinished items, the user had to type
// "continue" by hand to grant a fresh budget. Now the app does it for them:
// when a turn ends AFTER a tool-budget finalization AND unfinished todos
// remain, a synthetic continuation is queued through the normal type-ahead
// path (a fresh turn = a fresh budget).
//
// The quota protection stays intact: consecutive auto-continuations are
// capped at maxBudgetAutoContinues per user request (a REAL user message
// resets the streak), a queued user message always wins (we never cut in
// line), and the discuss-gate/plan flows are unaffected because the synthetic
// message is a plain action imperative.

const (
	// maxBudgetAutoContinues bounds consecutive synthetic continuations per
	// real user request: worst case (1 + max) × budget tool calls, then the
	// turn genuinely ends and the user decides.
	maxBudgetAutoContinues = 3

	// budgetAutoContinueMessage is the synthetic prompt. Kept imperative so
	// intent classification routes it as ACTION, and explicit about not
	// re-planning so the fresh turn resumes instead of restarting.
	budgetAutoContinueMessage = "Continue the unfinished tasks from your todo list. Pick up exactly where you stopped — do not re-plan from scratch and do not redo completed items."
)

// noteToolBudgetExhausted is the ExecutionHandler.OnToolBudgetExhausted sink —
// runs on the executor goroutine, so it only flips an atomic.
func (a *App) noteToolBudgetExhausted() {
	a.turnToolBudgetHit.Store(true)
}

// resetBudgetAutoContinueOnUserInput clears the streak when a REAL user
// message arrives — the cap is per user request, not per session.
func (a *App) resetBudgetAutoContinueOnUserInput(message string) {
	if message != budgetAutoContinueMessage {
		atomic.StoreInt32(&a.budgetAutoContinues, 0)
	}
}

// maybeAutoContinueAfterBudget runs at end of turn. Returns true when a
// synthetic continuation was queued.
func (a *App) maybeAutoContinueAfterBudget() bool {
	if !a.turnToolBudgetHit.Swap(false) {
		return false
	}
	// Interactive foreground only: headless/eval runs one prompt, and /loop
	// iterations have their own continuation machinery (handoff).
	if a.program == nil {
		return false
	}
	// The user already queued their own follow-up — never cut in line.
	if a.pendingCount() > 0 {
		return false
	}
	if a.executor == nil {
		return false
	}
	pending, _ := tools.IncompleteTodoSummary(a.executor.Registry())
	if pending == 0 {
		return false
	}
	n := atomic.AddInt32(&a.budgetAutoContinues, 1)
	if n > maxBudgetAutoContinues {
		a.safeSendToProgram(ui.StatusUpdateMsg{
			Type: ui.StatusWarning,
			Message: fmt.Sprintf(
				"Tool budget hit %d turns in a row with %d task(s) still open — pausing auto-continue; say \"continue\" to keep going",
				maxBudgetAutoContinues+1, pending),
		})
		return false
	}
	logging.Info("tool budget exhausted with unfinished todos — auto-continuing",
		"pending_todos", pending, "auto_continue", n, "cap", maxBudgetAutoContinues)
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type: ui.StatusInfo,
		Message: fmt.Sprintf("Tool budget reached — auto-continuing %d unfinished task(s) (%d/%d)",
			pending, n, maxBudgetAutoContinues),
	})
	// Queue through the normal type-ahead path: handleSubmit decides
	// queue-vs-process exactly like a typed follow-up would.
	a.safeGo("budget-auto-continue", func() {
		a.handleSubmitWithIntent(budgetAutoContinueMessage, false)
	})
	return true
}
