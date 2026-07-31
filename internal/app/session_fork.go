package app

import (
	"fmt"
	"time"

	"gokin/internal/chat"
	"gokin/internal/logging"
)

// SelectNewSessionID replaces the generated identity of a fresh startup
// session. It is intentionally narrower than SetID: changing an established
// conversation's persistence identity must go through resume/fork ownership.
func (a *App) SelectNewSessionID(newID string) error {
	if a == nil || a.session == nil {
		return fmt.Errorf("cannot select a session ID without an active session")
	}
	if err := chat.ValidateSessionID(newID); err != nil {
		return err
	}
	state := a.session.GetState()
	if sessionStateHasDurableWork(state) {
		return fmt.Errorf("cannot replace the identity of a session that already contains work")
	}
	a.session.SetID(newID)
	if a.executor != nil {
		a.executor.SetSessionID(newID)
	}
	// An explicitly requested identity is a deliberate session selection. Without
	// this, Run()'s startup auto-resume would still load this workspace's most
	// recent conversation and silently discard the ID the caller asked for —
	// exactly the surprise --session-id exists to prevent.
	a.sessionPreloaded = true
	return nil
}

// ForkLoadedSession publishes the currently preloaded conversation under a new
// identity and synchronously persists it. Callers must hold writer leases for
// both the source and destination until this transaction succeeds.
func (a *App) ForkLoadedSession(newID string) error {
	if a == nil || a.session == nil {
		return fmt.Errorf("cannot fork without an active session")
	}
	if a.sessionManager == nil {
		return fmt.Errorf("cannot fork without session persistence")
	}

	// PrepareSessionFork mutates the state it is handed and returns that same
	// graph, so the rollback snapshot must be an INDEPENDENT GetState() — using
	// the argument would "roll back" to the fork itself, leaving the live
	// session under the new identity with the source's executor lineage already
	// cleared.
	sourceID := a.session.GetID()
	rollbackState := a.session.GetState()
	forkState, err := chat.PrepareSessionFork(a.session.GetState(), newID, time.Now())
	if err != nil {
		return err
	}

	// Validate the complete copied graph before changing the live owner.
	probe := chat.NewSession()
	if err := probe.RestoreFromState(forkState); err != nil {
		return fmt.Errorf("validate fork session %q: %w", newID, err)
	}

	err = a.sessionManager.ApplyAndSave(func() (func(), error) {
		if err := a.session.RestoreFromState(forkState); err != nil {
			return nil, fmt.Errorf("restore fork session %q: %w", newID, err)
		}
		return func() {
			if restoreErr := a.session.RestoreFromState(rollbackState); restoreErr != nil {
				logging.Error("failed to roll back in-memory session fork",
					"source_session_id", sourceID,
					"fork_session_id", newID,
					"error", restoreErr)
			}
		}, nil
	})
	if err != nil {
		return fmt.Errorf("persist fork session %q: %w", newID, err)
	}

	a.mu.Lock()
	a.scratchpad = a.session.GetScratchpad()
	a.sessionPreloaded = true
	a.mu.Unlock()
	if a.agentRunner != nil {
		a.agentRunner.SetSharedScratchpad(a.scratchpad)
	}
	if a.executor != nil {
		a.executor.SetSessionID(newID)
	}
	a.restoreToolCheckpoints()
	logging.Info("forked session",
		"source_session_id", sourceID,
		"session_id", newID,
		"messages", len(forkState.History))
	return nil
}
