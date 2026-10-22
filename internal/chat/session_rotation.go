package chat

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/goals"
)

// RotateConversation preserves the current transcript without executable
// retries, then publishes an independent fresh root. The caller holds writer
// leases for both identities. All writes run under the normal saver lock.
//
// The fresh snapshot is saved first with a newer fixed activity timestamp.
// A crash before the source commit cannot destroy the source; a crash after
// that commit cannot resurrect its cancelled mutations. The empty fresh root
// also prevents automatic startup from falling back to the cleared dialogue.
// committed is true once source retry invalidation became visible, including
// commit-uncertain directory sync failures; callers must finish the rotation.
func (sm *SessionManager) RotateConversation(fresh *SessionState) (committed bool, err error) {
	if sm == nil || sm.session == nil || fresh == nil {
		return false, fmt.Errorf("conversation rotation requires two sessions")
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if !sm.config.Enabled {
		return false, ErrSessionPersistenceDisabled
	}
	previous := sm.session.GetState()
	if fresh.ID == previous.ID {
		return false, fmt.Errorf("fresh conversation requires a new session identity")
	}
	fresh.Version = previous.Version + 1
	// Both documents are validated before the first file is replaced.
	source := sm.session.GetState()
	source.PendingRecoveries = nil
	source.ToolCheckpoints = nil
	source.Goal = goals.Clear(source.Goal, "Cleared with conversation", time.Now())
	if !fresh.LastActive.After(source.LastActive) {
		fresh.LastActive = source.LastActive.Add(1)
	}
	for _, state := range []*SessionState{source, fresh} {
		probe := NewSession()
		if err := probe.RestoreFromState(state); err != nil {
			return false, err
		}
		if err := validateJSONPayloadForSave(state, maxSessionFileBytes); err != nil {
			return false, err
		}
	}
	if _, loadErr := sm.historyManager.LoadFull(fresh.ID); !errors.Is(loadErr, os.ErrNotExist) {
		if loadErr == nil {
			return false, fmt.Errorf("fresh session identity already exists")
		}
		return false, fmt.Errorf("cannot inspect fresh session identity: %w", loadErr)
	}
	if err := sm.historyManager.saveFullState(fresh); err != nil {
		// Only our newly reserved identity may be cleaned up. The live source and
		// its pending generation are untouched until the next save succeeds.
		_ = sm.historyManager.DeleteSession(fresh.ID)
		return false, fmt.Errorf("save fresh conversation: %w", err)
	}
	sourceErr := sm.historyManager.saveFullState(source)
	if sourceErr != nil && !errors.Is(sourceErr, fileutil.ErrAtomicWriteCommitUncertain) {
		cleanupErr := sm.historyManager.DeleteSession(fresh.ID)
		return false, errors.Join(fmt.Errorf("save previous conversation: %w", sourceErr), cleanupErr)
	}
	if err := sm.session.RestoreFromState(fresh); err != nil {
		return true, fmt.Errorf("publish validated fresh conversation: %w", err)
	}
	sm.lastSaveTime = fresh.LastActive
	return true, sourceErr
}
