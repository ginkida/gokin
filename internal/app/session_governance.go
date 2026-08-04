package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gokin/internal/chat"
	ctxutil "gokin/internal/context"
	"gokin/internal/ui"
	"google.golang.org/genai"
)

const (
	sessionGovernanceSoftLimit = 85
	sessionGovernanceKeepTail  = 65
)

var sessionArchiveBeforeAppendForTest func()

type sessionArchiveRecord struct {
	Timestamp       time.Time                `json:"ts"`
	SessionID       string                   `json:"session_id"`
	Reason          string                   `json:"reason"`
	ArchivedCount   int                      `json:"archived_count"`
	ArchivedSummary string                   `json:"archived_summary"`
	Messages        []chat.SerializedContent `json:"messages"`
}

func (a *App) enforceSessionMemoryGovernance(reason string) {
	if a == nil || a.session == nil {
		return
	}
	a.sessionLeaseMu.Lock()
	a.sessionGovernanceMu.Lock()
	archiveCount, stale, err := a.archiveSessionHistoryLocked(reason)
	a.sessionGovernanceMu.Unlock()
	a.sessionLeaseMu.Unlock()

	if err != nil {
		a.journalEvent("session_archive_failed", map[string]any{
			"error": err.Error(),
		})
		return
	}
	if stale {
		a.journalEvent("session_archive_stale_snapshot", map[string]any{
			"archived_messages": archiveCount,
			"reason":            reason,
		})
		return
	}
	if archiveCount == 0 {
		return
	}
	a.journalSessionArchive(archiveCount, reason)
	a.safeSendToProgram(ui.StreamTextMsg(fmt.Sprintf(
		"\n🗄️ Session governance archived %d old messages to keep context stable.\n",
		archiveCount)))
}

// archiveSessionHistoryLocked durably archives and then conditionally trims one
// session snapshot. Caller holds sessionLeaseMu and sessionGovernanceMu.
func (a *App) archiveSessionHistoryLocked(reason string) (archiveCount int, stale bool, err error) {
	history, sessionVersion := a.session.GetHistoryWithVersion()
	if len(history) <= sessionGovernanceSoftLimit {
		return 0, false, nil
	}
	archiveCount = len(history) - sessionGovernanceKeepTail
	if archiveCount <= 0 {
		return 0, false, nil
	}

	// Adjust boundary so FunctionCall/FunctionResponse pairs are not split
	archiveCount = ctxutil.AdjustBoundaryForToolPairs(history, archiveCount)
	if archiveCount <= 0 {
		return 0, false, nil
	}

	toArchive := history[:archiveCount]
	serialized := make([]chat.SerializedContent, 0, len(toArchive))
	for _, c := range toArchive {
		if c == nil {
			continue
		}
		serialized = append(serialized, chat.SerializeContent(c))
	}

	record := sessionArchiveRecord{
		Timestamp:       time.Now(),
		SessionID:       a.session.GetID(),
		Reason:          reason,
		ArchivedCount:   len(serialized),
		ArchivedSummary: summarizeArchivedMessages(toArchive),
		Messages:        serialized,
	}

	if err := a.appendSessionArchive(record); err != nil {
		return archiveCount, false, err
	}

	if !a.session.SetHistoryIfVersion(history[archiveCount:], sessionVersion) {
		return archiveCount, true, nil
	}
	return archiveCount, false, nil
}

func (a *App) appendSessionArchive(rec sessionArchiveRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	a.sessionArchiveMu.Lock()
	defer a.sessionArchiveMu.Unlock()
	if sessionArchiveBeforeAppendForTest != nil {
		sessionArchiveBeforeAppendForTest()
	}
	return appendSessionArchiveData(a.workDir, rec.SessionID, b)
}

func summarizeArchivedMessages(contents []*genai.Content) string {
	if len(contents) == 0 {
		return ""
	}
	var parts []string
	for _, c := range contents {
		if c == nil || len(c.Parts) == 0 {
			continue
		}
		text := strings.TrimSpace(c.Parts[0].Text)
		if text == "" {
			continue
		}
		if runes := []rune(text); len(runes) > 80 {
			text = string(runes[:77]) + "..."
		}
		parts = append(parts, text)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " | ")
}
