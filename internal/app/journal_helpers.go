package app

import (
	"strings"
	"time"
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

func previewForJournal(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= 160 {
		return s
	}
	return string(runes[:157]) + "..."
}
