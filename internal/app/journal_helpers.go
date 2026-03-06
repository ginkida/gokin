package app

import (
	"strings"
	"time"

	"gokin/internal/chat"
)

func (a *App) journalEvent(event string, details map[string]any) {
	if a == nil || a.journal == nil {
		return
	}
	a.journal.Append(event, details)
}

func (a *App) saveRecoverySnapshot(pendingMessage string) {
	if a == nil || a.journal == nil || a.session == nil {
		return
	}

	snap := RecoverySnapshot{
		SessionID:      a.session.ID,
		PendingMessage: pendingMessage,
		HistoryLen:     a.session.MessageCount(),
	}

	a.mu.Lock()
	snap.Processing = a.processing
	a.mu.Unlock()

	if a.planManager != nil {
		if p := a.planManager.GetCurrentPlan(); p != nil {
			snap.PlanID = p.ID
		}
		snap.CurrentStepID = a.planManager.GetCurrentStepID()
	}
	a.journal.SaveRecovery(snap)
}

func (a *App) journalSessionArchive(archived int, reason string) {
	a.sessionArchiveMu.Lock()
	a.sessionArchivedMessages += archived
	a.sessionArchiveOperations++
	a.lastSessionArchive = time.Now()
	a.sessionArchiveMu.Unlock()
	a.journalEvent("session_archive", map[string]any{
		"archived_messages": archived,
		"reason":            reason,
	})
}

// restoreToolCheckpoints loads persisted tool checkpoints from the session back
// into the executor's checkpoint journal after session restore.
func (a *App) restoreToolCheckpoints() {
	if a == nil || a.executor == nil || a.session == nil {
		return
	}
	checkpoints := a.session.GetToolCheckpoints()
	if len(checkpoints) == 0 {
		return
	}
	journal := a.executor.GetCheckpointJournal()
	if journal == nil {
		return
	}
	journal.Clear()
	for _, cp := range checkpoints {
		journal.RecordSerialized(cp.CallID, cp.ToolName, cp.Args, cp.Result, cp.Signature, cp.Timestamp)
	}
}

// syncToolCheckpoints snapshots the executor's checkpoint journal into the session
// for cross-session persistence. Called before session saves.
func (a *App) syncToolCheckpoints() {
	if a == nil || a.executor == nil || a.session == nil {
		return
	}
	journal := a.executor.GetCheckpointJournal()
	if journal == nil {
		return
	}
	entries := journal.Entries()
	if len(entries) == 0 {
		return
	}
	serialized := make([]chat.SerializedToolCheckpoint, len(entries))
	for i, e := range entries {
		serialized[i] = chat.SerializedToolCheckpoint{
			CallID:    e.CallID,
			ToolName:  e.ToolName,
			Args:      e.Args,
			Result:    e.Result.Content,
			Signature: e.Signature,
			Timestamp: e.Timestamp,
		}
	}
	a.session.SetToolCheckpoints(serialized)
}

func previewForJournal(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= 160 {
		return s
	}
	return string(runes[:157]) + "..."
}
