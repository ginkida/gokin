package app

import (
	"context"
	"fmt"

	"gokin/internal/logging"
	"gokin/internal/permission"
	"gokin/internal/tools"
)

// Discuss-mode (v0.100.51): the foreground "don't jump to implementation during
// analysis" guard. The user can analyze/discuss a problem with the agent without
// it suddenly editing code; the FIRST code-mutating tool in an analysis turn
// pauses for ONE confirm, after which the turn proceeds. A clear imperative
// ("implement it" / "сделай") classifies the turn as action and never gates.
//
// Concurrency: turnDiscuss/discussConfirmed are read+written ONLY on the turn
// goroutine. processMessageWithContext sets them at turn start; the executor
// (running synchronously inside that turn) reads discussGate() and, on the first
// mutation, calls actionConfirmPrompt which BLOCKS the same goroutine until the
// user answers, then flips discussConfirmed. No other goroutine touches them, so
// no lock — and crucially discussGate() must NOT take a.mu (it can be called deep
// inside the turn pipeline where a.mu may be held).

// discussStanceBanner is injected into turn-context when the turn is analysis.
// It tells the model the stance directly (belt-and-suspenders with the executor
// gate). Kept compact; lives in ephemeral turn-context, never the cached prefix.
const discussStanceBanner = "## Conversation stance: ANALYSIS (not implementation)\n" +
	"The user is analyzing/discussing right now — they have NOT asked you to implement yet. " +
	"Investigate, read, run read-only checks, explain, and PROPOSE concrete options with tradeoffs. " +
	"Do NOT write or edit code, or make repo changes, unless the user explicitly asks " +
	"(e.g. \"implement it\", \"go ahead\", \"сделай\"). If you think a change is warranted, " +
	"describe exactly what you'd do and ASK first rather than starting to edit."

// beginTurnIntent classifies the turn (analysis vs implement) and resets the
// per-turn confirm latch. Foreground-interactive ONLY: with no TUI (headless /
// eval / scripts) there is no human to discuss with, so it always acts.
func (a *App) beginTurnIntent(message string) {
	if a == nil {
		return
	}
	if a.program == nil { // headless / eval / non-interactive
		a.turnDiscuss.Store(false)
		a.discussConfirmed.Store(false)
		return
	}
	// YOLO / permissions-off means "act without asking" — honor that for the
	// discuss-mode gate too. A user who turned off permission prompts does NOT
	// expect a one-time "confirm you want to implement" prompt on the first edit
	// (the field report that motivated this). Disabling here (turnDiscuss=false)
	// keeps it consistent end-to-end: the executor's Step 4.7 gate never fires AND
	// the incomplete-work nudge stays active (both read discussGate()). The
	// out-of-workspace gate (Step 4.6) is a separate SECURITY boundary and is
	// deliberately NOT affected. Same "permissions off" signal as
	// updateUnrestrictedModeLocked; permManager is boot-set, IsEnabled is locked.
	if a.permManager == nil || !a.permManager.IsEnabled() {
		a.turnDiscuss.Store(false)
		a.discussConfirmed.Store(false)
		return
	}
	mode := ""
	if a.taskRouter != nil {
		mode = a.taskRouter.GetConversationMode()
	}
	a.turnDiscuss.Store(tools.ClassifyTurnDiscuss(message, mode))
	a.discussConfirmed.Store(false)
}

// discussGate reports whether the current turn is an analysis/discussion the
// user has NOT yet confirmed for implementation — i.e. the next code-mutating
// tool should pause for a confirm. Wired into the executor (Step 4.7 + the
// incomplete-work nudge suppression). Lock-free by design (turn-goroutine only).
func (a *App) discussGate() bool {
	if a == nil {
		return false
	}
	return a.turnDiscuss.Load() && !a.discussConfirmed.Load()
}

// actionConfirmPrompt is the ask-once shown before the first code-mutating tool
// in a discuss-mode turn. On allow it flips the turn to action (discussConfirmed)
// so the rest of the turn proceeds without further prompts; on deny the agent
// stays in analysis. Mirrors dirGrantPrompt: headless auto-allows (no stdin to
// block on), interactive reuses the permission modal.
func (a *App) actionConfirmPrompt(ctx context.Context, toolName, target string) (bool, error) {
	if a == nil {
		return true, nil
	}
	// No interactive program (headless/eval): act (symmetric with the dir-grant
	// and permission auto-allow). The gate is a foreground UX feature.
	if a.program == nil {
		a.discussConfirmed.Store(true)
		return true, nil
	}

	what := toolName
	if target != "" {
		what = fmt.Sprintf("%s %s", toolName, target)
	}
	req := &permission.Request{
		ToolName:  "implement during analysis",
		Args:      map[string]any{"action": what},
		RiskLevel: permission.RiskMedium,
		Reason: fmt.Sprintf(
			"We've been analyzing/discussing, and the agent is about to start IMPLEMENTING (%s). Allow to switch to action for the rest of this turn — or deny to keep analyzing. Tip: say \"implement it\" / \"сделай\" up front to skip this.",
			what),
	}
	decision, err := a.promptPermission(ctx, req)
	if err != nil {
		return false, err
	}
	switch decision {
	case permission.DecisionAllow, permission.DecisionAllowSession:
		a.discussConfirmed.Store(true) // rest of the turn proceeds as action
		logging.Info("discuss-mode: user confirmed implementation", "action", what)
		return true, nil
	default:
		logging.Info("discuss-mode: user kept analysis (declined implementation)", "action", what)
		return false, nil
	}
}
