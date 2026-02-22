package context

import (
	"math"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

// fileEvent records a single tool interaction with a file.
type fileEvent struct {
	MessageIndex int
	ToolName     string
	IsWrite      bool
}

// FileActivityTracker tracks file-level tool activity for scoring.
type FileActivityTracker struct {
	mu              sync.RWMutex
	fileEvents      map[string][]fileEvent // path → events
	externalChanges map[string]time.Time   // path → timestamp of external change
}

// NewFileActivityTracker creates a new tracker.
func NewFileActivityTracker() *FileActivityTracker {
	return &FileActivityTracker{
		fileEvents:      make(map[string][]fileEvent),
		externalChanges: make(map[string]time.Time),
	}
}

// RecordToolCall records a tool call that interacts with a file.
func (t *FileActivityTracker) RecordToolCall(toolName string, args map[string]any, msgIndex int) {
	filePath := extractFilePath(args)
	if filePath == "" {
		return
	}

	isWrite := isWriteTool(toolName)

	t.mu.Lock()
	defer t.mu.Unlock()

	t.fileEvents[filePath] = append(t.fileEvents[filePath], fileEvent{
		MessageIndex: msgIndex,
		ToolName:     toolName,
		IsWrite:      isWrite,
	})
}

// RecordExternalChange marks a file as externally modified.
func (t *FileActivityTracker) RecordExternalChange(filePath string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.externalChanges[filePath] = time.Now()
}

// Reset clears all tracked activity.
func (t *FileActivityTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fileEvents = make(map[string][]fileEvent)
	t.externalChanges = make(map[string]time.Time)
}

// getEvents returns events for a file (caller must hold at least RLock).
func (t *FileActivityTracker) getEvents(filePath string) []fileEvent {
	return t.fileEvents[filePath]
}

// hasExternalChange returns whether the file was externally modified (caller must hold at least RLock).
func (t *FileActivityTracker) hasExternalChange(filePath string) bool {
	_, ok := t.externalChanges[filePath]
	return ok
}

// RelevanceScorer computes importance scores for history messages
// using multi-signal analysis: tool type, recency, edit proximity,
// reference frequency, staleness, errors, and tool pair bonuses.
type RelevanceScorer struct{}

// NewRelevanceScorer creates a new scorer.
func NewRelevanceScorer() *RelevanceScorer {
	return &RelevanceScorer{}
}

// baseToolScores maps tool names to their base importance (0–3).
var baseToolScores = map[string]float64{
	"write":      3.0,
	"edit":       3.0,
	"bash":       2.5,
	"delete":     2.5,
	"copy":       2.0,
	"move":       2.0,
	"git_commit": 2.5,
	"git_add":    2.0,
	"ask_user":   1.5,
	"grep":       1.0,
	"glob":       0.5,
	"read":       0.5,
	"list_dir":   0.3,
	"tree":       0.3,
}

// ScoreMessages scores each message in history for importance.
// Returns a float64 slice aligned with the history slice (0–10 range).
// Use indexOffset when scoring a sub-slice of history so that EditProximityBonus
// can compare against FileActivityTracker indices correctly.
func (s *RelevanceScorer) ScoreMessages(history []*genai.Content, tracker *FileActivityTracker, indexOffset ...int) []float64 {
	n := len(history)
	scores := make([]float64, n)
	if n == 0 {
		return scores
	}

	// Pre-compute: collect all file references per message for frequency bonus
	msgFiles := make([][]string, n)
	fileRefCount := make(map[string]int)
	for i, msg := range history {
		if msg == nil {
			continue
		}
		files := extractFileRefs(msg)
		msgFiles[i] = files
		for _, f := range files {
			fileRefCount[f]++
		}
	}

	// Collect FunctionResponse IDs for pair bonus
	responseIDs := make(map[string]bool)
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responseIDs[part.FunctionResponse.ID] = true
			}
		}
	}

	// Collect FunctionCall IDs for pair bonus
	callIDs := make(map[string]bool)
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part != nil && part.FunctionCall != nil && part.FunctionCall.ID != "" {
				callIDs[part.FunctionCall.ID] = true
			}
		}
	}

	if tracker != nil {
		tracker.mu.RLock()
		defer tracker.mu.RUnlock()
	}

	// When scoring a sub-slice of history, indexOffset maps local indices
	// to global history indices so EditProximityBonus works correctly.
	off := 0
	if len(indexOffset) > 0 {
		off = indexOffset[0]
	}

	for i, msg := range history {
		if msg == nil {
			continue
		}
		score := s.scoreMessage(msg, i, i+off, n, msgFiles[i], fileRefCount, responseIDs, callIDs, tracker)
		scores[i] = score
	}

	return scores
}

// ScoreMessage scores a single message given its index and total count.
func (s *RelevanceScorer) ScoreMessage(msg *genai.Content, msgIndex, totalMessages int, tracker *FileActivityTracker) float64 {
	if msg == nil {
		return 0
	}

	files := extractFileRefs(msg)
	refCount := make(map[string]int)
	for _, f := range files {
		refCount[f] = 1
	}

	if tracker != nil {
		tracker.mu.RLock()
		defer tracker.mu.RUnlock()
	}

	return s.scoreMessage(msg, msgIndex, msgIndex, totalMessages, files, refCount, nil, nil, tracker)
}

// scoreMessage is the internal scoring engine. Caller must hold tracker.mu.RLock if tracker != nil.
// localIdx is position within the scored slice (for recency).
// globalIdx is position in the full history (for EditProximityBonus vs tracker events).
func (s *RelevanceScorer) scoreMessage(
	msg *genai.Content,
	localIdx, globalIdx, total int,
	files []string,
	fileRefCount map[string]int,
	responseIDs map[string]bool,
	callIDs map[string]bool,
	tracker *FileActivityTracker,
) float64 {
	var score float64

	// 1. BaseToolScore (0–3)
	baseScore := s.computeBaseToolScore(msg)
	score += baseScore

	// 2. RecencyBonus (0–2): linear ramp within scored slice, newer = higher
	if total > 1 {
		score += 2.0 * (float64(localIdx) / float64(total-1))
	}

	// 3. EditProximityBonus (0–2): read followed by edit on same file
	// Uses globalIdx to match against FileActivityTracker event indices.
	if tracker != nil {
		score += s.computeEditProximityBonus(msg, globalIdx, tracker)
	}

	// 4. ReferenceFrequencyBonus (0–1.5)
	maxRefBonus := 0.0
	for _, f := range files {
		bonus := math.Min(1.5, 0.3*float64(fileRefCount[f]))
		if bonus > maxRefBonus {
			maxRefBonus = bonus
		}
	}
	score += maxRefBonus

	// 5. StalenessDiscount (-2–0): file changed externally after read
	if tracker != nil {
		for _, f := range files {
			if tracker.hasExternalChange(f) {
				score -= 2.0
				break // Apply at most once
			}
		}
	}

	// 6. ErrorBonus (0–1.5)
	score += s.computeErrorBonus(msg)

	// 7. PairBonus (0–1): FunctionCall with matching Response or vice versa
	score += s.computePairBonus(msg, responseIDs, callIDs)

	// Clamp to [0, 10]
	if score < 0 {
		score = 0
	}
	if score > 10 {
		score = 10
	}

	return score
}

// computeBaseToolScore returns 0–3 based on tool type.
func (s *RelevanceScorer) computeBaseToolScore(msg *genai.Content) float64 {
	best := 0.0
	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		if part.FunctionCall != nil {
			if v, ok := baseToolScores[part.FunctionCall.Name]; ok && v > best {
				best = v
			}
		}
		if part.FunctionResponse != nil {
			if v, ok := baseToolScores[part.FunctionResponse.Name]; ok && v > best {
				best = v
			}
		}
	}
	return best
}

// computeEditProximityBonus returns 0–2 if a read is close to a subsequent edit on the same file.
// Caller must hold tracker.mu.RLock.
func (s *RelevanceScorer) computeEditProximityBonus(msg *genai.Content, msgIdx int, tracker *FileActivityTracker) float64 {
	// Find files referenced by this message
	files := extractFileRefs(msg)
	if len(files) == 0 {
		return 0
	}

	best := 0.0
	for _, filePath := range files {
		events := tracker.getEvents(filePath)
		// Find read events at/before msgIdx and write events after
		hasRead := false
		minWriteDistance := -1

		for _, ev := range events {
			if !ev.IsWrite && ev.MessageIndex <= msgIdx {
				hasRead = true
			}
			if ev.IsWrite && ev.MessageIndex > msgIdx {
				dist := ev.MessageIndex - msgIdx
				if minWriteDistance < 0 || dist < minWriteDistance {
					minWriteDistance = dist
				}
			}
		}

		if hasRead && minWriteDistance > 0 {
			bonus := 2.0 / (1.0 + float64(minWriteDistance)/10.0)
			if bonus > best {
				best = bonus
			}
		}
	}
	return best
}

// computeErrorBonus returns 0–1.5 if the message contains error indicators.
func (s *RelevanceScorer) computeErrorBonus(msg *genai.Content) float64 {
	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		// Check FunctionResponse for error field
		if part.FunctionResponse != nil && part.FunctionResponse.Response != nil {
			if errMsg, ok := part.FunctionResponse.Response["error"].(string); ok && errMsg != "" {
				return 1.5
			}
		}
		// Check text for error keywords
		if part.Text != "" {
			lower := strings.ToLower(part.Text)
			if strings.Contains(lower, "error") || strings.Contains(lower, "failed") ||
				strings.Contains(lower, "panic") || strings.Contains(lower, "exception") {
				return 1.0
			}
		}
	}
	return 0
}

// computePairBonus returns 0–1 if the message has a FunctionCall with a matching Response (or vice versa).
func (s *RelevanceScorer) computePairBonus(msg *genai.Content, responseIDs, callIDs map[string]bool) float64 {
	if responseIDs == nil && callIDs == nil {
		return 0
	}
	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		if part.FunctionCall != nil && part.FunctionCall.ID != "" {
			if responseIDs != nil && responseIDs[part.FunctionCall.ID] {
				return 1.0
			}
		}
		if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
			if callIDs != nil && callIDs[part.FunctionResponse.ID] {
				return 1.0
			}
		}
	}
	return 0
}

// extractFileRefs extracts file paths referenced in a message (from tool args and text).
func extractFileRefs(msg *genai.Content) []string {
	seen := make(map[string]bool)
	var files []string

	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		// From FunctionCall args
		if part.FunctionCall != nil {
			if path := extractFilePath(part.FunctionCall.Args); path != "" && !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
	}
	return files
}

// extractFilePath gets the file path from tool arguments.
func extractFilePath(args map[string]any) string {
	if args == nil {
		return ""
	}
	if path, ok := args["file_path"].(string); ok && path != "" {
		return path
	}
	if path, ok := args["path"].(string); ok && path != "" {
		return path
	}
	return ""
}

// isWriteTool returns true if the tool modifies files.
func isWriteTool(name string) bool {
	switch name {
	case "write", "edit", "delete", "copy", "move", "mkdir", "bash", "git_add", "git_commit":
		return true
	}
	return false
}
