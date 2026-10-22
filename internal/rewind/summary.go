package rewind

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gokin/internal/chat"
	"gokin/internal/skills"

	"google.golang.org/genai"
)

const MaxSummaries = 1024

type SummaryRef struct {
	ID     string `json:"id"`
	Blob   string `json:"blob"`
	Marker string `json:"marker"`
}

// SummaryArchive keeps original messages independently of checkpoint pruning.
// Marker matching uses its entire content, so user text resembling a summary
// cannot accidentally expand into a different conversation.
type SummaryArchive struct {
	ID       string                   `json:"id"`
	PointID  string                   `json:"point_id"`
	Mode     string                   `json:"mode"`
	Marker   chat.SerializedContent   `json:"marker"`
	Original []chat.SerializedContent `json:"original"`
	// Replacement is the exact compacted span. Older targeted summaries replace
	// one message, while automatic compaction can retain/reorder messages inside
	// the span. Expanding only Marker would duplicate those retained messages.
	Replacement []chat.SerializedContent `json:"replacement,omitempty"`
}
type SummaryPlan struct {
	ID, PointID, Mode, Prompt, SessionID string
	Before, Messages, After              []chat.SerializedContent
	Source                               string
}

func validSummaryID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func validSummaryArchive(archive SummaryArchive, ref SummaryRef) bool {
	if archive.ID != ref.ID || !validSummaryID(archive.ID) || !validSummaryID(archive.PointID) || len(archive.Original) == 0 || len(archive.Original) > 100000 || contentHash(archive.Marker) != ref.Marker {
		return false
	}
	if archive.Mode == "compact" {
		if len(archive.Replacement) == 0 || len(archive.Replacement) > chat.MaxMessages || contentHash(archive.Replacement[0]) != ref.Marker {
			return false
		}
		for _, part := range archive.Marker.Parts {
			if part.Type == "text" && strings.HasPrefix(part.Text, "[Compacted conversation: "+archive.ID+"]\n") {
				return true
			}
		}
		return false
	}
	if len(archive.Replacement) != 0 {
		return false
	}
	if archive.Mode != "summarize-from" && archive.Mode != "summarize-before" {
		return false
	}
	return archive.Marker.Role == string(genai.RoleUser) && len(archive.Marker.Parts) == 1 && archive.Marker.Parts[0].Type == "text" && strings.HasPrefix(archive.Marker.Parts[0].Text, "[Summarized conversation: "+archive.ID+"]")
}

func contentHash(value any) string {
	data, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func rawContents(history []chat.SerializedContent) ([]chat.SerializedContent, error) {
	contents := make([]*genai.Content, len(history))
	for i, item := range history {
		c, err := chat.DeserializeContent(item)
		if err != nil {
			return nil, err
		}
		contents[i] = c
	}
	contents = skills.ReattachInvocations(contents, nil, 0)
	raw := make([]chat.SerializedContent, len(contents))
	for i, c := range contents {
		raw[i] = chat.SerializeContent(c)
	}
	return raw, nil
}

// PlanSummary resolves a point against the current transcript, expanding only
// registered summary spans. Unchanged context outside the selected interval
// retains its current representation. A boundary inside a previous summary
// expands just the unaffected portion, preserving its original messages.
func (s *Store) PlanSummary(id, mode string, state *chat.SessionState) (*SummaryPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode != "summarize-from" && mode != "summarize-before" {
		return nil, fmt.Errorf("unknown summary direction")
	}
	if state == nil || state.ID != s.doc.SessionID {
		return nil, fmt.Errorf("summary session mismatch")
	}
	if err := s.importHistoryWindows(state.HistoryWindows); err != nil {
		return nil, err
	}
	if len(s.doc.Summaries) >= MaxSummaries {
		return nil, fmt.Errorf("session has reached the summary archive limit")
	}
	var point *Point
	for i := range s.doc.Points {
		if s.doc.Points[i].ID == id {
			point = &s.doc.Points[i]
			break
		}
	}
	if point == nil {
		return nil, fmt.Errorf("checkpoint not found")
	}
	var snapshot Snapshot
	if err := s.readBlob(point.Snapshot, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.State == nil || snapshot.State.ID != state.ID {
		return nil, fmt.Errorf("checkpoint snapshot identity mismatch")
	}
	if err := s.importHistoryWindows(snapshot.State.HistoryWindows); err != nil {
		return nil, err
	}
	current, err := rawContents(state.History)
	if err != nil {
		return nil, err
	}
	prior, err := rawContents(snapshot.State.History)
	if err != nil {
		return nil, err
	}
	blocks, err := s.transcriptBlocks(current)
	if err != nil {
		return nil, err
	}
	priorBlocks, err := s.transcriptBlocks(prior)
	if err != nil {
		return nil, err
	}
	expanded, checkpoint := flattenBlocks(blocks), flattenBlocks(priorBlocks)
	boundary := len(checkpoint)
	if boundary > len(expanded) || boundary > 0 && contentHash(checkpoint) != contentHash(expanded[:boundary]) {
		return nil, fmt.Errorf("checkpoint does not match the current conversation; restore that point before summarizing it")
	}
	start, end := boundary, len(expanded)
	if mode == "summarize-before" {
		start, end = 0, boundary
	}
	if start == end {
		return nil, fmt.Errorf("there are no messages in the selected summary range")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	plan := &SummaryPlan{ID: hex.EncodeToString(nonce[:]), PointID: id, Mode: mode, Prompt: snapshot.Prompt, SessionID: state.ID, Source: contentHash(state.History), Messages: expanded[start:end]}
	offset := 0
	for _, block := range blocks {
		next := offset + len(block.original)
		switch {
		case next <= start:
			plan.Before = append(plan.Before, block.current...)
		case offset >= end:
			plan.After = append(plan.After, block.current...)
		default:
			if offset < start {
				plan.Before = append(plan.Before, block.original[:start-offset]...)
			}
			if next > end {
				plan.After = append(plan.After, block.original[end-offset:]...)
			}
		}
		offset = next
	}
	return plan, nil
}

// SaveSummary publishes an immutable original transcript before the condensed
// context is saved. If session publication later fails this extra archive is
// harmless, and no original text is lost. File events and point snapshots are
// never rewritten by context summarization.
func (s *Store) SaveSummary(plan *SummaryPlan, marker chat.SerializedContent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if plan == nil || plan.SessionID != s.doc.SessionID || !validSummaryID(plan.ID) || len(plan.Messages) == 0 {
		return fmt.Errorf("invalid summary archive")
	}
	if len(s.doc.Summaries) >= MaxSummaries {
		return fmt.Errorf("session has reached the summary archive limit")
	}
	archive := SummaryArchive{ID: plan.ID, PointID: plan.PointID, Mode: plan.Mode, Marker: marker, Original: plan.Messages}
	return s.saveSummaryLocked(archive)
}

func (s *Store) saveSummaryLocked(archive SummaryArchive) error {
	if len(s.doc.Summaries) >= MaxSummaries {
		return fmt.Errorf("session has reached the summary archive limit")
	}
	for _, ref := range s.doc.Summaries {
		if ref.ID == archive.ID {
			return fmt.Errorf("summary already exists")
		}
	}
	if !validSummaryArchive(archive, SummaryRef{ID: archive.ID, Marker: contentHash(archive.Marker)}) {
		return fmt.Errorf("invalid summary archive contents")
	}
	hash, err := s.writeBlob(archive)
	if err != nil {
		return err
	}
	before := len(s.doc.Summaries)
	s.doc.Summaries = append(s.doc.Summaries, SummaryRef{ID: archive.ID, Blob: hash, Marker: contentHash(archive.Marker)})
	if err := s.save(); err != nil {
		s.doc.Summaries = s.doc.Summaries[:before]
		return err
	}
	return nil
}

func (s *Store) Summary(id string) (SummaryArchive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range s.doc.Summaries {
		if ref.ID == id {
			var archive SummaryArchive
			err := s.readBlob(ref.Blob, &archive)
			if err != nil {
				return archive, err
			}
			if !validSummaryArchive(archive, ref) {
				return SummaryArchive{}, fmt.Errorf("summary archive identity mismatch")
			}
			return archive, nil
		}
	}
	return SummaryArchive{}, fmt.Errorf("summary archive not found")
}

// Matches checks the conversation captured for planning without treating a
// concurrently saved retry or token count as a different transcript.
func (p *SummaryPlan) Matches(state *chat.SessionState) bool {
	return p != nil && state != nil && state.ID == p.SessionID && contentHash(state.History) == p.Source
}
