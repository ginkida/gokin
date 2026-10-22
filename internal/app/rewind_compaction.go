package app

import (
	"context"
	"fmt"

	"gokin/internal/chat"

	"google.golang.org/genai"
)

// commitRewindCompaction is shared by automatic/manual summarization,
// emergency truncation and old tool-output cleanup. Archive publication comes
// before the history CAS; a concurrent turn or session switch can leave an
// unused archive, but can never lose current messages or publish a stale edit.
func (a *App) commitRewindCompaction(ctx context.Context, history []*genai.Content, version int64, carryAt int, reason string) ([]*genai.Content, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !a.sessionManager.PersistenceEnabled() {
		a.sessionLeaseMu.Lock()
		defer a.sessionLeaseMu.Unlock()
		committed, ok := a.session.SetHistoryIfVersionWithSkillCarry(history, version, carryAt)
		if ok {
			a.startSessionStartLocked("compact", false)
		}
		return committed, ok, nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	initial := a.session.GetState()
	if initial.Version != version {
		return nil, false, nil
	}
	if a.sessionLease == nil {
		lease, err := chat.AcquireSessionWriterLease(initial.ID)
		if err != nil {
			return nil, false, err
		}
		a.sessionLease = lease
	}
	if !a.sessionLease.IsActive() || a.sessionLease.SessionID() != initial.ID {
		return nil, false, fmt.Errorf("context compaction does not own the session writer lease")
	}
	store, err := a.turnRewindStore()
	if err != nil {
		return nil, false, err
	}
	// Compute the exact message window and skill carry before archiving. The
	// archive adds a reference inside a message, so it consumes no extra slot.
	probe := chat.NewSession()
	if err := probe.RestoreFromState(initial); err != nil {
		return nil, false, err
	}
	probe.SetHistoryWithSkillCarry(history, carryAt)
	archived, err := store.ArchiveCompaction(initial.History, probe.GetState().History)
	if err != nil {
		return nil, false, fmt.Errorf("preserve original context before %s: %w", reason, err)
	}
	history = make([]*genai.Content, len(archived))
	for i, item := range archived {
		history[i], err = chat.DeserializeContent(item)
		if err != nil {
			return nil, false, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	committed, ok := a.session.SetHistoryIfVersionWithSkillCarry(history, version, carryAt)
	if !ok {
		return nil, false, nil
	}
	// The originals are already durable. A session save failure leaves both
	// the live compacted context and the older on-disk transcript recoverable.
	// Surface it separately instead of rolling back over a concurrent append.
	saveErr := a.sessionManager.Save()
	if saveErr != nil {
		a.recordHeadlessTerminalOutcome("persistence_failed", fmt.Sprintf("compacted session could not be saved: %v", saveErr))
	}
	a.surfacePersistenceResult("session_compaction", "Context changed but its session snapshot could not be saved", saveErr)
	a.startSessionStartLocked("compact", false)
	return committed, true, nil
}
