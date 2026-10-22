package chat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"gokin/internal/skills"

	"google.golang.org/genai"
)

// HistoryWindowArchive is a pending, session-owned context rewrite. It stays
// in the session snapshot until its originals have reached the rewind journal.
// This makes an interrupted/failed handoff recoverable without doing file I/O
// while holding Session.mu.
type HistoryWindowArchive struct {
	ID     string              `json:"id"`
	Before []SerializedContent `json:"before"`
	After  []SerializedContent `json:"after"`
}

func (s *Session) SetHistoryWindowHandler(handler func(sessionID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.windowHistoryHandler = handler
}

func (s *Session) PendingHistoryWindows() (string, []HistoryWindowArchive) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ID, cloneHistoryWindows(s.windowArchives)
}

// AcknowledgeHistoryWindows removes only records durably transferred for this
// identity. A callback from an older turn cannot clear another session's queue.
func (s *Session) AcknowledgeHistoryWindows(id string, windows []HistoryWindowArchive) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ID != id || len(windows) == 0 {
		return
	}
	done := make(map[string]bool, len(windows))
	for _, frame := range windows {
		done[frame.ID] = true
	}
	var pending []HistoryWindowArchive
	for _, frame := range s.windowArchives {
		if !done[frame.ID] {
			pending = append(pending, frame)
		}
	}
	s.windowArchives = pending
}

func cloneHistoryWindows(windows []HistoryWindowArchive) []HistoryWindowArchive {
	if len(windows) == 0 {
		return nil
	}
	cloned := make([]HistoryWindowArchive, len(windows))
	for i, frame := range windows {
		cloned[i] = frame
		cloned[i].Before = cloneWindowContents(frame.Before)
		cloned[i].After = cloneWindowContents(frame.After)
	}
	return cloned
}

func cloneWindowContents(contents []SerializedContent) []SerializedContent {
	cloned := append([]SerializedContent(nil), contents...)
	for i := range cloned {
		cloned[i].Parts = append([]SerializedPart(nil), contents[i].Parts...)
		for j := range cloned[i].Parts {
			part := &cloned[i].Parts[j]
			part.ThoughtSignature = append([]byte(nil), part.ThoughtSignature...)
			for _, function := range []**SerializedFunc{&part.FunctionCall, &part.FunctionResp} {
				if *function != nil {
					copy := **function
					if copy.Args != nil {
						copy.Args = cloneSerializedJSONValue(copy.Args).(map[string]any)
					}
					if copy.Response != nil {
						copy.Response = cloneSerializedJSONValue(copy.Response).(map[string]any)
					}
					*function = &copy
				}
			}
			if part.Structured != nil {
				part.Structured = cloneStructuredPart(part.Structured)
			}
		}
	}
	return cloned
}

func serializeWindowHistory(history []*genai.Content) []SerializedContent {
	serialized := make([]SerializedContent, len(history))
	for i, content := range history {
		serialized[i] = SerializeContent(content)
	}
	return serialized
}

// Caller holds s.mu. Only publication paths use this wrapper; restoring a
// saved snapshot uses the pure preparation helper and does not invent edits.
func (s *Session) prepareWindowHistoryLocked(history []*genai.Content, insertAt int) []*genai.Content {
	committed := s.prepareHistoryWithSkillCarryLocked(history, insertAt)
	return s.captureHistoryWindowLocked(history, committed)
}

func (s *Session) captureHistoryWindowLocked(history, committed []*genai.Content) []*genai.Content {
	if s.windowHistoryHandler == nil {
		return committed
	}
	before := skills.ReattachInvocations(history, nil, 0)
	after := skills.ReattachInvocations(committed, nil, 0)
	if len(before) <= len(after) {
		return committed
	}
	if len(after) == 0 {
		after = []*genai.Content{{Role: genai.RoleUser}}
		committed = after
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(fmt.Sprintf("cannot reserve history archive identity: %v", err))
	}
	id := hex.EncodeToString(nonce[:])
	first := *after[0]
	first.Parts = append(append([]*genai.Part(nil), first.Parts...), genai.NewPartFromText(fmt.Sprintf("[Compacted conversation: %s]\nOriginal messages: use read with file_path gokin://summary/%s (offset/limit supported).", id, id)))
	for i, content := range committed {
		if content == after[0] {
			committed = append([]*genai.Content(nil), committed...)
			committed[i] = &first
			break
		}
	}
	after = append([]*genai.Content(nil), after...)
	after[0] = &first
	s.windowArchives = append(s.windowArchives, HistoryWindowArchive{ID: id, Before: cloneWindowContents(serializeWindowHistory(before)), After: cloneWindowContents(serializeWindowHistory(after))})
	return committed
}

// TrimHistoryPrefixIfVersion is the explicit soft-window counterpart to the
// automatic hard limit. Both transfer originals through the same owner queue.
func (s *Session) TrimHistoryPrefixIfVersion(keepFrom int, expectedVersion int64) bool {
	s.mu.Lock()
	if s.version != expectedVersion || keepFrom < 0 || keepFrom > len(s.History) {
		s.mu.Unlock()
		return false
	}
	oldCount := len(s.History)
	committed := s.prepareHistoryWithSkillCarryLocked(s.History[keepFrom:], 0)
	committed = s.captureHistoryWindowLocked(s.History, committed)
	s.History = committed
	s.tokenCounts = make([]int, len(committed))
	s.totalTokens = 0
	s.version++
	s.notifyChange(oldCount)
	return true
}

func ValidateHistoryWindows(windows []HistoryWindowArchive) error {
	if len(windows) > 1024 {
		return fmt.Errorf("too many pending history windows")
	}
	seen := make(map[string]bool, len(windows))
	for _, frame := range windows {
		if len(frame.ID) != 32 || seen[frame.ID] || len(frame.Before) == 0 || len(frame.Before) > 100000 || len(frame.After) == 0 || len(frame.After) > MaxMessages || len(frame.After) >= len(frame.Before) {
			return fmt.Errorf("invalid pending history window")
		}
		if _, err := hex.DecodeString(frame.ID); err != nil {
			return fmt.Errorf("invalid pending history window identity")
		}
		seen[frame.ID] = true
		marker := false
		for _, part := range frame.After[0].Parts {
			marker = marker || part.Type == "text" && strings.HasPrefix(part.Text, "[Compacted conversation: "+frame.ID+"]\n")
		}
		if !marker {
			return fmt.Errorf("pending history window has no original-message reference")
		}
	}
	return nil
}
