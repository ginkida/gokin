package context

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/client"
	"gokin/internal/logging"

	"google.golang.org/genai"
)

// MessagePriority represents the importance level of a message.
type MessagePriority int

const (
	PriorityLow      MessagePriority = 0 // Verbose logs, trivial reads
	PriorityNormal   MessagePriority = 1 // Normal messages
	PriorityHigh     MessagePriority = 2 // File edits, important decisions
	PriorityCritical MessagePriority = 3 // System prompts, errors
)

// MessageScore represents the importance score and metadata for a message.
type MessageScore struct {
	Priority    MessagePriority
	Score       float64 // 0.0 - 1.0
	Reason      string  // Explanation of the score
	IsSystem    bool
	HasFileEdit bool
	HasError    bool
	ToolsUsed   []string
	References  []string // File paths, function names
}

// MessageScorer evaluates message importance for context retention decisions.
// Supports both keyword-based heuristic scoring (default) and LLM-based
// semantic scoring when a client is configured.
type MessageScorer struct {
	// Tools that should be considered high priority
	criticalTools map[string]bool
	// Tools that should be considered low priority
	verboseTools map[string]bool

	// Semantic scoring via LLM (optional)
	semanticMu      sync.RWMutex
	semanticClient  client.Client
	semanticTimeout time.Duration

	// Incremental semantic score cache: per-message hash → cached score.
	// Avoids re-scoring messages that haven't changed between compaction cycles.
	scoreCacheMu sync.RWMutex
	scoreCache   map[string]cachedScore
	scoreCacheTTL time.Duration
}

// cachedScore stores a semantic score with expiry time.
type cachedScore struct {
	score     float64
	expiresAt time.Time
}

// NewMessageScorer creates a new message scorer with default configuration.
func NewMessageScorer() *MessageScorer {
	return &MessageScorer{
		criticalTools: map[string]bool{
			"write": true,
			"edit":  true,
			"bash":  true,
		},
		verboseTools: map[string]bool{
			"read":        true,
			"list_dir":    true,
			"tree":        true,
			"glob":        true,
			"git_log":     true,
			"env":         true,
			"task_output": true,
		},
		scoreCache:    make(map[string]cachedScore),
		scoreCacheTTL: 15 * time.Minute,
	}
}

// ScoreMessage evaluates a single message and returns its importance score.
func (s *MessageScorer) ScoreMessage(msg *genai.Content) MessageScore {
	score := MessageScore{
		Priority:   PriorityNormal,
		Score:      0.5,
		Reason:     "normal message",
		ToolsUsed:  make([]string, 0),
		References: make([]string, 0),
	}

	// Check role
	if msg.Role == genai.RoleUser {
		// User messages are generally important
		score.Score += 0.1
	}

	// Analyze parts
	for _, part := range msg.Parts {
		s.scoreTextPart(&score, part.Text)
		s.scoreFunctionCall(&score, part.FunctionCall)
		s.scoreFunctionResponse(&score, part.FunctionResponse)
	}

	// Determine final priority based on score
	if score.IsSystem || score.HasError {
		score.Priority = PriorityCritical
		score.Score = 1.0
	} else if score.HasFileEdit {
		score.Priority = PriorityHigh
		score.Score = 0.8
	} else if score.Score < 0.3 {
		score.Priority = PriorityLow
	} else if score.Score > 0.7 {
		score.Priority = PriorityHigh
	}

	return score
}

// scoreTextPart analyzes text content for importance indicators.
func (s *MessageScorer) scoreTextPart(score *MessageScore, text string) {
	if text == "" {
		return
	}

	lower := strings.ToLower(text)

	// Check for system indicators
	if strings.Contains(lower, "system prompt") ||
		strings.Contains(lower, "instructions") ||
		strings.Contains(lower, "context preservation") {
		score.IsSystem = true
		score.Reason = "system instructions"
		score.Score += 0.4
	}

	// Check for decision keywords
	decisionKeywords := []string{
		"decided to", "will implement", "going to", "plan to",
		"summary", "conclusion", "resolved", "fixed",
	}
	for _, keyword := range decisionKeywords {
		if strings.Contains(lower, keyword) {
			score.Score += 0.1
		}
	}

	// Check for error indicators
	errorKeywords := []string{
		"error", "failed", "exception", "bug", "issue",
		"problem", "warning", "not found",
	}
	for _, keyword := range errorKeywords {
		if strings.Contains(lower, keyword) {
			score.HasError = true
			score.Score += 0.2
		}
	}

	// Check for file references (simple heuristic)
	if strings.Contains(text, ".go") ||
		strings.Contains(text, ".md") ||
		strings.Contains(text, ".yaml") ||
		strings.Contains(text, ".json") {
		// Extract potential file paths
		words := strings.Fields(text)
		for _, word := range words {
			if strings.Contains(word, "/") &&
				(strings.HasSuffix(word, ".go") ||
					strings.HasSuffix(word, ".md") ||
					strings.HasSuffix(word, ".yaml") ||
					strings.HasSuffix(word, ".json")) {
				score.References = append(score.References, strings.Trim(word, "`'\""))
			}
		}
	}
}

// scoreFunctionCall analyzes function calls for importance.
func (s *MessageScorer) scoreFunctionCall(score *MessageScore, fc *genai.FunctionCall) {
	if fc == nil {
		return
	}

	score.ToolsUsed = append(score.ToolsUsed, fc.Name)

	// Check if it's a critical tool (file modifications)
	if s.criticalTools[fc.Name] {
		score.HasFileEdit = true
		score.Score += 0.3
		score.Reason = "file modification operation"

		// Extract file paths from args
		if path, ok := fc.Args["file_path"].(string); ok {
			score.References = append(score.References, path)
		}
		if path, ok := fc.Args["path"].(string); ok {
			score.References = append(score.References, path)
		}
	}

	// Check if it's a verbose tool (information gathering)
	if s.verboseTools[fc.Name] {
		score.Score -= 0.1
		if score.Score < 0.2 {
			score.Score = 0.2
		}
		if score.Reason == "normal message" {
			score.Reason = "information gathering"
		}
	}
}

// scoreFunctionResponse analyzes function responses for importance.
func (s *MessageScorer) scoreFunctionResponse(score *MessageScore, fr *genai.FunctionResponse) {
	if fr == nil {
		return
	}

	// Check for errors in response
	if fr.Response != nil {
		if errMsg, ok := fr.Response["error"].(string); ok && errMsg != "" {
			score.HasError = true
			score.Score += 0.3
		}

		// Check content size - very large responses are less important
		if content, ok := fr.Response["content"].(string); ok {
			if len(content) > 5000 {
				score.Score -= 0.1
			}
		}

		// Check for success indicators
		if success, ok := fr.Response["success"].(bool); ok && success {
			score.Score += 0.05
		}
	}
}

// ScoreMessages scores a batch of messages.
// When semantic scoring is enabled, automatically uses LLM-based evaluation
// with a background context (10s timeout). Falls back to heuristic scoring.
func (s *MessageScorer) ScoreMessages(messages []*genai.Content) []MessageScore {
	// Try semantic scoring if enabled
	s.semanticMu.RLock()
	enabled := s.semanticClient != nil
	s.semanticMu.RUnlock()

	if enabled && len(messages) >= 6 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.ScoreMessagesWithContext(ctx, messages)
	}

	scores := make([]MessageScore, len(messages))
	for i, msg := range messages {
		scores[i] = s.ScoreMessage(msg)
	}
	return scores
}

// SelectImportantMessages selects messages to keep based on scores and target count.
// It uses a combination of priority and recency to make decisions.
func (s *MessageScorer) SelectImportantMessages(
	messages []*genai.Content,
	scores []MessageScore,
	keepCount int,
) []*genai.Content {
	if len(messages) <= keepCount {
		return messages
	}

	// Always keep critical priority messages
	selected := make([]*genai.Content, 0, keepCount)
	selectedIndices := make(map[int]bool)

	// First pass: add all critical messages
	for i, score := range scores {
		if score.Priority == PriorityCritical && len(selected) < keepCount {
			selected = append(selected, messages[i])
			selectedIndices[i] = true
		}
	}

	// Second pass: add high priority messages
	for i, score := range scores {
		if score.Priority == PriorityHigh && !selectedIndices[i] && len(selected) < keepCount {
			selected = append(selected, messages[i])
			selectedIndices[i] = true
		}
	}

	// Third pass: fill with most recent messages if space remains
	if len(selected) < keepCount {
		// Start from the end (most recent)
		for i := len(messages) - 1; i >= 0 && len(selected) < keepCount; i-- {
			if !selectedIndices[i] {
				selected = append(selected, messages[i])
				selectedIndices[i] = true
			}
		}
	}

	return selected
}

// CalculateTokenBudget calculates how many tokens should be allocated for messages
// based on their importance scores.
func (s *MessageScorer) CalculateTokenBudget(
	scores []MessageScore,
	totalBudget int,
) []int {
	if len(scores) == 0 {
		return []int{}
	}

	// Calculate total score
	totalScore := 0.0
	for _, score := range scores {
		totalScore += float64(score.Priority) + score.Score
	}

	// Allocate budget proportionally
	budgets := make([]int, len(scores))
	for i, score := range scores {
		weight := (float64(score.Priority) + score.Score) / totalScore
		budgets[i] = int(float64(totalBudget) * weight)
		if budgets[i] < 100 { // Minimum allocation
			budgets[i] = 100
		}
	}

	return budgets
}

// SetSemanticClient enables LLM-based semantic scoring.
func (s *MessageScorer) SetSemanticClient(c client.Client) {
	s.semanticMu.Lock()
	defer s.semanticMu.Unlock()
	s.semanticClient = c
	if s.semanticTimeout == 0 {
		s.semanticTimeout = 10 * time.Second
	}
}

// SetSemanticTimeout sets the timeout for semantic scoring API calls.
func (s *MessageScorer) SetSemanticTimeout(d time.Duration) {
	s.semanticMu.Lock()
	defer s.semanticMu.Unlock()
	s.semanticTimeout = d
}

// hashMessage generates a SHA256 hash for a single message, used as cache key.
func hashMessage(msg *genai.Content) string {
	h := sha256.New()
	h.Write([]byte(msg.Role))
	for _, part := range msg.Parts {
		if part.Text != "" {
			text := part.Text
			if len(text) > 500 {
				text = text[:500]
			}
			h.Write([]byte(text))
		}
		if part.FunctionCall != nil {
			h.Write([]byte("fc:" + part.FunctionCall.Name))
		}
		if part.FunctionResponse != nil {
			h.Write([]byte("fr:" + part.FunctionResponse.Name))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// getCachedSemanticScore returns a cached semantic score if available and not expired.
func (s *MessageScorer) getCachedSemanticScore(hash string) (float64, bool) {
	s.scoreCacheMu.RLock()
	defer s.scoreCacheMu.RUnlock()
	if cached, ok := s.scoreCache[hash]; ok {
		if time.Now().Before(cached.expiresAt) {
			return cached.score, true
		}
	}
	return 0, false
}

// setCachedSemanticScore stores a semantic score in the cache.
func (s *MessageScorer) setCachedSemanticScore(hash string, score float64) {
	s.scoreCacheMu.Lock()
	defer s.scoreCacheMu.Unlock()

	// Evict expired entries if cache grows too large (>500).
	// Scan up to 50 entries per write to amortize cost.
	if len(s.scoreCache) > 500 {
		now := time.Now()
		evicted := 0
		for k, v := range s.scoreCache {
			if now.After(v.expiresAt) {
				delete(s.scoreCache, k)
			}
			evicted++
			if evicted >= 50 {
				break
			}
		}
	}

	s.scoreCache[hash] = cachedScore{
		score:     score,
		expiresAt: time.Now().Add(s.scoreCacheTTL),
	}
}

// semanticScoringPrompt is the prompt template for LLM-based message scoring.
const semanticScoringPrompt = `Score these conversation messages by importance for context retention.
Return a JSON array of objects with "index" (0-based) and "score" (0.0-1.0).

Scoring criteria:
- 1.0: Critical decisions, error resolutions, architectural choices, key discoveries
- 0.8: File modifications (write/edit), important tool results with actionable info
- 0.6: Useful context (file reads with relevant content, successful test results)
- 0.4: Routine information gathering (glob, tree, list_dir results)
- 0.2: Acknowledgments, confirmations, redundant reads of already-seen files
- 0.1: Empty responses, verbose logs, repeated tool calls with same results

MESSAGES:
%s

Return ONLY the JSON array, no other text.`

// semanticScoreResult represents a single score from the LLM response.
type semanticScoreResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// ScoreMessagesWithContext uses LLM-based semantic scoring when available,
// falling back to heuristic scoring. Uses incremental caching to avoid
// re-scoring messages that haven't changed between compaction cycles.
func (s *MessageScorer) ScoreMessagesWithContext(ctx context.Context, messages []*genai.Content) []MessageScore {
	// Start with heuristic scores (fast)
	heuristicScores := make([]MessageScore, len(messages))
	for i, msg := range messages {
		heuristicScores[i] = s.ScoreMessage(msg)
	}

	s.semanticMu.RLock()
	c := s.semanticClient
	timeout := s.semanticTimeout
	s.semanticMu.RUnlock()

	if c == nil || len(messages) < 6 {
		return heuristicScores
	}

	// Check cache for each message — only LLM-score uncached ones
	messageHashes := make([]string, len(messages))
	uncachedIndices := make([]int, 0)

	for i, msg := range messages {
		h := hashMessage(msg)
		messageHashes[i] = h
		if cachedScore, ok := s.getCachedSemanticScore(h); ok {
			// Apply cached semantic score blended with heuristic
			heuristicScores[i].Score = 0.3*heuristicScores[i].Score + 0.7*cachedScore
			s.updatePriority(&heuristicScores[i])
			heuristicScores[i].Reason = "cached+" + heuristicScores[i].Reason
		} else {
			uncachedIndices = append(uncachedIndices, i)
		}
	}

	// If all messages are cached, skip LLM call entirely
	if len(uncachedIndices) == 0 {
		logging.Debug("semantic scoring: all messages cached", "count", len(messages))
		return heuristicScores
	}

	// If only a few uncached messages, skip LLM call (not enough context)
	if len(uncachedIndices) < 3 {
		logging.Debug("semantic scoring: too few uncached messages", "uncached", len(uncachedIndices))
		return heuristicScores
	}

	logging.Debug("semantic scoring: calling LLM",
		"total", len(messages),
		"uncached", len(uncachedIndices),
		"cached", len(messages)-len(uncachedIndices))

	// Build a compact representation of ALL messages (LLM needs context)
	// but only request scores for uncached indices
	var builder strings.Builder
	for i, msg := range messages {
		role := "User"
		if msg.Role == genai.RoleModel {
			role = "Assistant"
		}
		for _, part := range msg.Parts {
			if part.Text != "" {
				text := part.Text
				if len(text) > 200 {
					text = text[:200] + "..."
				}
				fmt.Fprintf(&builder, "[%d] %s: %s\n", i, role, text)
			}
			if part.FunctionCall != nil {
				fmt.Fprintf(&builder, "[%d] %s: [tool_call: %s]\n", i, role, part.FunctionCall.Name)
			}
			if part.FunctionResponse != nil {
				content := "[result]"
				if respContent, ok := part.FunctionResponse.Response["content"].(string); ok {
					if len(respContent) > 100 {
						content = respContent[:100] + "..."
					} else {
						content = respContent
					}
				}
				if errMsg, ok := part.FunctionResponse.Response["error"].(string); ok && errMsg != "" {
					content = "Error: " + errMsg
				}
				fmt.Fprintf(&builder, "[%d] Tool(%s): %s\n", i, part.FunctionResponse.Name, content)
			}
		}
	}

	prompt := fmt.Sprintf(semanticScoringPrompt, builder.String())

	scoreCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := c.SendMessage(scoreCtx, prompt)
	if err != nil {
		logging.Debug("semantic scoring failed, using heuristics", "error", err)
		return heuristicScores
	}

	resp, err := stream.Collect()
	if err != nil {
		logging.Debug("semantic scoring collect failed", "error", err)
		return heuristicScores
	}

	// Parse LLM response
	var llmScores []semanticScoreResult
	text := strings.TrimSpace(resp.Text)
	text = strings.TrimPrefix(text, "```json")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)

	if err := json.Unmarshal([]byte(text), &llmScores); err != nil {
		logging.Debug("semantic scoring parse failed", "error", err, "text", text)
		return heuristicScores
	}

	// Apply LLM scores and cache them
	for _, ls := range llmScores {
		if ls.Index >= 0 && ls.Index < len(heuristicScores) {
			heuristic := heuristicScores[ls.Index].Score
			semantic := ls.Score
			heuristicScores[ls.Index].Score = 0.3*heuristic + 0.7*semantic
			s.updatePriority(&heuristicScores[ls.Index])
			heuristicScores[ls.Index].Reason = "semantic+" + heuristicScores[ls.Index].Reason

			// Cache the raw semantic score for future lookups
			s.setCachedSemanticScore(messageHashes[ls.Index], ls.Score)
		}
	}

	return heuristicScores
}

// updatePriority updates message priority based on blended score.
func (s *MessageScorer) updatePriority(score *MessageScore) {
	if score.Score >= 0.8 {
		score.Priority = PriorityCritical
	} else if score.Score >= 0.6 {
		score.Priority = PriorityHigh
	} else if score.Score < 0.3 {
		score.Priority = PriorityLow
	}
}
