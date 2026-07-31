package chat

import (
	"fmt"
	"time"
)

// PrepareSessionFork turns an ownership-safe SessionState snapshot into a new
// conversation identity. Conversation context and user-created checkpoints are
// preserved, while executor recovery lineage is removed: a fork must never
// replay an interrupted side effect that belonged to its source session.
func PrepareSessionFork(state *SessionState, newID string, startedAt time.Time) (*SessionState, error) {
	if state == nil {
		return nil, fmt.Errorf("cannot fork an empty session state")
	}
	if err := ValidateSessionID(newID); err != nil {
		return nil, fmt.Errorf("invalid fork session ID: %w", err)
	}
	if state.ID == newID {
		return nil, fmt.Errorf("fork session ID %q matches the source session", newID)
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}

	// GetState returns an ownership-safe graph, so mutating this snapshot does
	// not affect the live source Session. Nested branches can carry the same
	// executor journals and must be scrubbed as well.
	state.ID = newID
	state.StartTime = startedAt
	state.LastActive = startedAt
	clearForkExecutionLineage(state)
	return state, nil
}

func clearForkExecutionLineage(state *SessionState) {
	if state == nil {
		return
	}
	state.ToolCheckpoints = nil
	state.PendingRecoveries = nil
	for _, branch := range state.Branches {
		clearForkExecutionLineage(branch)
	}
}
