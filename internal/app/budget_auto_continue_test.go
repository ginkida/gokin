package app

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

func newBudgetTestApp(t *testing.T, withTodos bool) *App {
	t.Helper()
	reg := tools.NewRegistry()
	todo := tools.NewTodoTool()
	if err := reg.Register(todo); err != nil {
		t.Fatal(err)
	}
	if withTodos {
		if _, err := todo.Execute(t.Context(), map[string]any{"todos": []any{
			map[string]any{"content": "finish mcp_admin actions", "status": "in_progress", "active_form": "Finishing"},
			map[string]any{"content": "add tests", "status": "pending", "active_form": "Adding"},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	exec := tools.NewExecutor(reg, nil, time.Second)
	return &App{executor: exec}
}

// v0.100.104 field report: a turn that exhausts the per-turn tool budget with
// unfinished todos made the user type "continue" by hand. The app now queues
// the continuation itself — bounded, and only when the budget actually hit.
func TestMaybeAutoContinueAfterBudget(t *testing.T) {
	// No budget hit this turn → no continuation.
	a := newBudgetTestApp(t, true)
	a.program = &tea.Program{}
	if a.maybeAutoContinueAfterBudget() {
		t.Fatal("must not continue without a budget hit")
	}

	// Budget hit + unfinished todos → queues exactly once and consumes the flag.
	a.noteToolBudgetExhausted()
	if !a.maybeAutoContinueAfterBudget() {
		t.Fatal("budget hit with unfinished todos must auto-continue")
	}
	if a.turnToolBudgetHit.Load() {
		t.Fatal("flag must be consumed")
	}
	if a.maybeAutoContinueAfterBudget() {
		t.Fatal("second call without a new budget hit must not continue")
	}

	// Budget hit but NO unfinished todos → no continuation (work is done).
	b := newBudgetTestApp(t, false)
	b.program = &tea.Program{}
	b.noteToolBudgetExhausted()
	if b.maybeAutoContinueAfterBudget() {
		t.Fatal("no unfinished todos → the turn genuinely ended, no continuation")
	}

	// Cap: the streak stops at maxBudgetAutoContinues.
	c := newBudgetTestApp(t, true)
	c.program = &tea.Program{}
	atomic.StoreInt32(&c.budgetAutoContinues, maxBudgetAutoContinues)
	c.noteToolBudgetExhausted()
	if c.maybeAutoContinueAfterBudget() {
		t.Fatal("streak past the cap must pause auto-continue")
	}

	// A REAL user message resets the streak; the synthetic one does not.
	c.resetBudgetAutoContinueOnUserInput(budgetAutoContinueMessage)
	if atomic.LoadInt32(&c.budgetAutoContinues) == 0 {
		t.Fatal("synthetic message must NOT reset the streak")
	}
	c.resetBudgetAutoContinueOnUserInput("продолжи")
	if got := atomic.LoadInt32(&c.budgetAutoContinues); got != 0 {
		t.Fatalf("real user message must reset the streak, got %d", got)
	}

	// Headless (no program) → never auto-continues.
	d := newBudgetTestApp(t, true)
	d.noteToolBudgetExhausted()
	if d.maybeAutoContinueAfterBudget() {
		t.Fatal("headless must not auto-continue")
	}

	// The synthetic prompt must be an action imperative that resumes, not
	// re-plans (intent classification + honest continuation).
	if !strings.Contains(budgetAutoContinueMessage, "Continue") ||
		!strings.Contains(budgetAutoContinueMessage, "do not re-plan") {
		t.Fatalf("synthetic prompt shape changed: %q", budgetAutoContinueMessage)
	}
}

// A budget flag left over from a turn that ended on an ERROR path (before the
// end-of-turn check consumed it) must not leak into the next turn — the
// turn-start reset in processMessageWithContext clears it. Pinned here at the
// primitive level: Store(false) + maybeAutoContinueAfterBudget = no-op.
func TestBudgetFlagClearedAtTurnStart(t *testing.T) {
	a := newBudgetTestApp(t, true)
	a.program = &tea.Program{}
	a.noteToolBudgetExhausted() // prior turn's leftover
	a.turnToolBudgetHit.Store(false)
	if a.maybeAutoContinueAfterBudget() {
		t.Fatal("cleared flag must not auto-continue")
	}
}
