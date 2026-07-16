package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"gokin/internal/chat"
	"gokin/internal/logging"
	"gokin/internal/ui"
)

// AdoptSessionWriterLease transfers ownership of lease to the App. The lease
// must protect the App's current in-memory session. After a successful call,
// callers must release it through ReleaseSessionWriterLease rather than by
// retaining and releasing the original pointer themselves.
func (a *App) AdoptSessionWriterLease(lease *chat.SessionWriterLease) error {
	if a == nil {
		return fmt.Errorf("cannot adopt a session writer lease without an app")
	}
	if lease == nil {
		return fmt.Errorf("cannot adopt a nil session writer lease")
	}
	if !lease.IsActive() {
		return fmt.Errorf("cannot adopt an inactive session writer lease")
	}
	if a.session == nil {
		return fmt.Errorf("cannot adopt a session writer lease without an active session")
	}

	currentID := a.session.GetID()
	if lease.SessionID() != currentID {
		return fmt.Errorf("writer lease protects session %q, but the active session is %q", lease.SessionID(), currentID)
	}

	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if activeID := a.session.GetID(); activeID != currentID {
		return fmt.Errorf("active session changed from %q to %q while adopting its writer lease", currentID, activeID)
	}
	if a.sessionLease == lease {
		return nil
	}
	if a.sessionLease != nil {
		return fmt.Errorf("app already owns a writer lease for session %q", a.sessionLease.SessionID())
	}
	a.sessionLease = lease
	return nil
}

// ReleaseSessionWriterLease releases the writer lease currently owned by the
// App. It is safe to call more than once.
func (a *App) ReleaseSessionWriterLease() error {
	if a == nil {
		return nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.sessionLease == nil {
		return nil
	}
	lease := a.sessionLease
	a.sessionLease = nil
	return lease.Release()
}

// SwitchSession transactionally moves the App to selected.ID while preserving
// exclusive writer ownership. The target snapshot is re-read only after its
// lease is acquired, the current session is synchronously flushed before any
// mutation, and every failure leaves the current session and lease in place.
//
// force bypasses the project/provider compatibility guards, matching
// `/resume <id> --force`; identity validation and writer exclusivity are never
// bypassed.
func (a *App) SwitchSession(ctx context.Context, selected *chat.SessionState, force bool) (*chat.SessionState, error) {
	if a == nil || a.session == nil {
		return nil, fmt.Errorf("cannot switch without an active session")
	}
	if a.sessionManager == nil {
		return nil, fmt.Errorf("cannot switch without a session manager")
	}
	if selected == nil {
		return nil, fmt.Errorf("cannot switch to an empty session state")
	}
	if ctx == nil {
		return nil, fmt.Errorf("cannot switch session without a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	targetID := selected.ID
	if err := chat.ValidateSessionID(targetID); err != nil {
		return nil, fmt.Errorf("switch session: %w", err)
	}

	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()

	currentID := a.session.GetID()
	currentLease := a.sessionLease
	if currentLease == nil {
		var err error
		currentLease, err = chat.AcquireSessionWriterLease(currentID)
		if err != nil {
			return nil, fmt.Errorf("acquire writer lease for current session %q: %w", currentID, err)
		}
		a.sessionLease = currentLease
	} else if !currentLease.IsActive() {
		return nil, fmt.Errorf("refusing session switch: owned writer lease for session %q is inactive", currentLease.SessionID())
	} else if currentLease.SessionID() != currentID {
		return nil, fmt.Errorf("refusing session switch: active session %q is not protected by the owned lease for %q", currentID, currentLease.SessionID())
	}

	// Resuming the already-active identity is a no-op, but only after proving
	// that this App owns its writer lease.
	if targetID == currentID {
		return a.session.GetState(), nil
	}

	targetLease, err := chat.AcquireSessionWriterLease(targetID)
	if err != nil {
		return nil, fmt.Errorf("acquire writer lease for target session %q: %w", targetID, err)
	}
	targetLeaseOwned := true
	defer func() {
		if targetLeaseOwned {
			if releaseErr := targetLease.Release(); releaseErr != nil {
				logging.Warn("failed to release unused target session writer lease", "session_id", targetID, "error", releaseErr)
			}
		}
	}()

	// The state supplied by the command/startup selector is only a candidate.
	// Re-read it while holding the target lease to close the selection/acquire
	// TOCTOU window. Non-force loads also enforce exact project ownership.
	var state *chat.SessionState
	if force {
		historyManager, historyErr := chat.NewHistoryManager()
		if historyErr != nil {
			return nil, fmt.Errorf("create history manager for target session %q: %w", targetID, historyErr)
		}
		state, err = historyManager.LoadFull(targetID)
	} else {
		state, _, err = a.sessionManager.LoadSession(targetID)
	}
	if err != nil {
		return nil, fmt.Errorf("reload target session %q under writer lease: %w", targetID, err)
	}
	if state == nil || state.ID != targetID {
		return nil, fmt.Errorf("reload target session %q returned mismatched identity", targetID)
	}
	if !force && (a.workDir == "" || state.WorkDir == "" || filepath.Clean(a.workDir) != filepath.Clean(state.WorkDir)) {
		return nil, fmt.Errorf("refusing target session %q from a different work directory", targetID)
	}

	a.mu.Lock()
	currentProvider := runtimeProviderForConfig(a.config)
	a.mu.Unlock()
	if !force && state.Provider != "" && currentProvider != "" && state.Provider != currentProvider {
		return nil, fmt.Errorf("%w: session %q was on provider %s but the current provider is %s", ErrSessionProviderMismatch, targetID, state.Provider, currentProvider)
	}

	// Validate the complete graph before touching the live Session. In
	// particular, a malformed nested branch can otherwise fail after the top-
	// level identity/history have already been replaced.
	probe := chat.NewSession()
	if err := probe.RestoreFromState(state); err != nil {
		return nil, fmt.Errorf("validate target session %q: %w", targetID, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A timer may have durably claimed a retry while this /resume command owned
	// the foreground, leaving the generation proven-unstarted in the FIFO (or in
	// the tiny claim->dispatch handoff). Return only those in-process-known claims
	// to scheduled before releasing the current session's writer lease. Ambiguous
	// claims restored from disk are intentionally untouched.
	releasedBeforeSwitch, err := a.releaseProvenUnstartedRecoveriesBeforeSwitchLocked(currentID)
	if err != nil {
		return nil, err
	}
	switchPublished := false
	defer func() {
		// The release transaction removed the FIFO/awaiting ownership and paused
		// timers. If any later flush/context/restore step aborts before the target
		// session is published, the old session is still active and must regain its
		// scheduled timers instead of becoming inert until restart.
		if releasedBeforeSwitch && !switchPublished && a.session != nil && a.session.GetID() == currentID {
			a.resumePersistedRecoveriesWithGrace(false, recoveryRedispatchGrace)
		}
	}()

	// Persist executor recovery state together with the conversation, then
	// synchronously drain the old session before changing its identity. An
	// empty fresh session carries no durable user work and is intentionally not
	// materialized, otherwise it would become the newest auto-resume candidate.
	a.syncToolCheckpoints()
	currentState := a.session.GetState()
	if a.session.GetID() != currentID {
		return nil, fmt.Errorf("active session identity changed from %q during switch", currentID)
	}
	if sessionStateHasDurableWork(currentState) {
		if err := a.sessionManager.Save(); err != nil {
			return nil, fmt.Errorf("flush current session %q before switch: %w", currentID, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := a.sessionManager.RestoreFromState(state); err != nil {
		rollbackErr := a.session.RestoreFromState(currentState)
		if rollbackErr != nil {
			return nil, errors.Join(
				fmt.Errorf("restore target session %q: %w", targetID, err),
				fmt.Errorf("rollback current session %q: %w", currentID, rollbackErr),
			)
		}
		return nil, fmt.Errorf("restore target session %q: %w", targetID, err)
	}

	// Publish the new owner before releasing the old lease. At every point in
	// the successful transition, each session that may be written is protected.
	a.sessionLease = targetLease
	targetLeaseOwned = false
	switchPublished = true
	if releaseErr := currentLease.Release(); releaseErr != nil {
		logging.Warn("failed to release previous session writer lease", "session_id", currentID, "error", releaseErr)
	}

	scratchpad := a.session.GetScratchpad()
	a.mu.Lock()
	a.scratchpad = scratchpad
	a.mu.Unlock()
	if a.agentRunner != nil {
		a.agentRunner.SetSharedScratchpad(scratchpad)
	}
	a.restoreToolCheckpoints()
	a.safeSendToProgram(ui.ScratchpadMsg(scratchpad))
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if running {
		a.resumePersistedRecoveries(false)
	}

	return state, nil
}

func sessionStateHasDurableWork(state *chat.SessionState) bool {
	if state == nil {
		return false
	}
	return len(state.History) > 0 ||
		state.Scratchpad != "" ||
		len(state.Branches) > 0 ||
		len(state.Checkpoints) > 0 ||
		len(state.ToolCheckpoints) > 0 ||
		len(state.PendingRecoveries) > 0 ||
		len(state.InvokedSkills) > 0 ||
		len(state.CheckpointInvokedSkills) > 0
}
