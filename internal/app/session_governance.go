package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/ui"
	"google.golang.org/genai"
)

const (
	sessionGovernanceSoftLimit = 85
	sessionGovernanceKeepTail  = 65
)

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
	history := a.session.GetHistory()
	if len(history) <= sessionGovernanceSoftLimit {
		return
	}
	archiveCount := len(history) - sessionGovernanceKeepTail
	if archiveCount <= 0 {
		return
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
		SessionID:       a.session.ID,
		Reason:          reason,
		ArchivedCount:   len(serialized),
		ArchivedSummary: summarizeArchivedMessages(toArchive),
		Messages:        serialized,
	}

	if err := a.appendSessionArchive(record); err != nil {
		a.journalEvent("session_archive_failed", map[string]any{
			"error": err.Error(),
		})
		return
	}

	a.session.SetHistory(history[archiveCount:])
	a.journalSessionArchive(archiveCount, reason)
	a.safeSendToProgram(ui.StreamTextMsg(fmt.Sprintf(
		"\n🗄️ Session governance archived %d old messages to keep context stable.\n",
		archiveCount)))
}

func (a *App) appendSessionArchive(rec sessionArchiveRecord) error {
	dir := filepath.Join(a.workDir, ".gokin", "session_archives")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, rec.SessionID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
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
		if len(text) > 80 {
			text = text[:77] + "..."
		}
		parts = append(parts, text)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " | ")
}
