package app

import (
	"fmt"
	"strings"

	"gokin/internal/logging"
	"gokin/internal/ui"
)

// surfacePersistenceResult warns once per failing streak while an interactive
// runtime can actually receive the warning. Failures before Program.Run are
// deliberately not latched: the first event after the TUI comes online will
// retry visibility instead of treating a message sent to nil as "notified".
func (a *App) surfacePersistenceResult(key, message string, err error) {
	if a == nil {
		return
	}
	if err == nil {
		a.persistenceFailures.shouldNotify(key, false)
		return
	}
	logging.Warn("persistence subsystem failing",
		"subsystem", key, "error", err)

	if !a.hasProgram() || !a.persistenceFailures.shouldNotify(key, true) {
		return
	}
	a.safeSendToProgramAsync(ui.StatusUpdateMsg{
		Type:    ui.StatusWarning,
		Message: message,
	})
}

// saveCurrentPlanWithVisibility persists the active plan and makes failure an
// observable runtime outcome. Callers in tool callbacks cannot abort a tool
// that already ran, but they must not silently lose its run-ledger/rollback
// metadata. Interactive mode warns once per failing streak; headless mode also
// records a terminal persistence error so automation cannot report exit 0.
func (a *App) saveCurrentPlanWithVisibility(reason string) error {
	if a == nil || a.planManager == nil {
		return nil
	}
	err := a.planManager.SaveCurrentPlan()
	const key = "current_plan"
	if err == nil {
		a.surfacePersistenceResult(key, "", nil)
		return nil
	}

	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "plan update"
	}
	message := fmt.Sprintf(
		"Plan persistence is failing after %s — resume/rollback metadata may be stale",
		reason)
	a.recordHeadlessTerminalOutcome("persistence_failed",
		fmt.Sprintf("%s: %v", message, err))
	a.surfacePersistenceResult(key, message, err)
	return err
}
