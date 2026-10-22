package app

import (
	"fmt"

	"gokin/internal/chat"
)

// History callbacks run outside Session.mu, but some publishers already own
// sessionLeaseMu. Defer that handoff rather than deadlocking: pending originals
// are part of the session snapshot and every rewind-store open retries it.
func (a *App) flushHistoryWindowArchives(id string) {
	if a.session == nil {
		return
	}
	current, pending := a.session.PendingHistoryWindows()
	if current != id || len(pending) == 0 {
		return
	}
	a.mu.Lock()
	stopping := a.shuttingDown
	a.mu.Unlock()
	if stopping {
		return
	}
	if !a.sessionLeaseMu.TryLock() {
		return
	}
	defer a.sessionLeaseMu.Unlock()
	if a.session == nil || a.session.GetID() != id {
		return
	}
	err := a.flushHistoryWindowsLocked(id)
	if err != nil {
		a.recordHeadlessTerminalOutcome("persistence_failed", fmt.Sprintf("history window originals could not be archived: %v", err))
	}
	a.surfacePersistenceResult("history_windows", "Could not archive the history window; originals are retained in the session and new model work is paused until storage recovers", err)
}

func (a *App) flushHistoryWindowsLocked(id string) error {
	if !a.sessionManager.PersistenceEnabled() {
		return nil
	}
	if a.sessionLease == nil {
		lease, err := chat.AcquireSessionWriterLease(id)
		if err != nil {
			return err
		}
		a.sessionLease = lease
	}
	if !a.sessionLease.IsActive() || a.sessionLease.SessionID() != id {
		return fmt.Errorf("history window does not own the session writer lease")
	}
	_, err := a.turnRewindStore()
	return err
}

func (a *App) ensureHistoryWindowsReady() error {
	id, pending := a.session.PendingHistoryWindows()
	if len(pending) == 0 || !a.sessionManager.PersistenceEnabled() {
		return nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.session.GetID() != id {
		return fmt.Errorf("history window conversation changed")
	}
	return a.flushHistoryWindowsLocked(id)
}
