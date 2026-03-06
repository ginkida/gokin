package context

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestMessageScorerScoreMessage(t *testing.T) {
	s := NewMessageScorer()

	// User message with decision keywords
	msg := genai.NewContentFromText("I decided to implement the new feature", genai.RoleUser)
	score := s.ScoreMessage(msg)
	if score.Score <= 0.5 {
		t.Errorf("decision keyword score = %f, want > 0.5", score.Score)
	}
}

func TestMessageScorerCriticalTool(t *testing.T) {
	s := NewMessageScorer()

	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "write",
					Args: map[string]any{"file_path": "/tmp/test.go"},
				},
			},
		},
	}

	score := s.ScoreMessage(msg)
	if !score.HasFileEdit {
		t.Error("write tool should set HasFileEdit")
	}
	if score.Priority != PriorityHigh {
		t.Errorf("write tool priority = %d, want PriorityHigh", score.Priority)
	}
	if len(score.References) == 0 {
		t.Error("should extract file path reference")
	}
}

func TestMessageScorerVerboseTool(t *testing.T) {
	s := NewMessageScorer()

	msg := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{
				FunctionCall: &genai.FunctionCall{
					Name: "read",
					Args: map[string]any{"file_path": "/tmp/test.go"},
				},
			},
		},
	}

	score := s.ScoreMessage(msg)
	if score.Score >= 0.5 {
		t.Errorf("verbose tool score = %f, should be < 0.5", score.Score)
	}
}

func TestMessageScorerErrorDetection(t *testing.T) {
	s := NewMessageScorer()

	msg := genai.NewContentFromText("Error: failed to compile the project", genai.RoleModel)
	score := s.ScoreMessage(msg)
	if !score.HasError {
		t.Error("should detect error keyword")
	}
	if score.Priority != PriorityCritical {
		t.Errorf("error priority = %d, want PriorityCritical", score.Priority)
	}
}

func TestMessageScorerSystemDetection(t *testing.T) {
	s := NewMessageScorer()

	msg := genai.NewContentFromText("Follow these system prompt instructions for context preservation", genai.RoleUser)
	score := s.ScoreMessage(msg)
	if !score.IsSystem {
		t.Error("should detect system indicators")
	}
}

func TestMessageScorerFunctionResponse(t *testing.T) {
	s := NewMessageScorer()

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "bash",
					Response: map[string]any{
						"error": "command not found",
					},
				},
			},
		},
	}

	score := s.ScoreMessage(msg)
	if !score.HasError {
		t.Error("should detect error in function response")
	}
}

func TestMessageScorerScoreMessages(t *testing.T) {
	s := NewMessageScorer()

	messages := []*genai.Content{
		genai.NewContentFromText("Hello", genai.RoleUser),
		genai.NewContentFromText("Hi there", genai.RoleModel),
		genai.NewContentFromText("Error: something failed", genai.RoleModel),
	}

	scores := s.ScoreMessages(messages)
	if len(scores) != 3 {
		t.Fatalf("scores = %d, want 3", len(scores))
	}

	// Error message should have highest score
	if scores[2].Score <= scores[0].Score {
		t.Error("error message should score higher than greeting")
	}
}

func TestMessageScorerSelectImportantMessages(t *testing.T) {
	s := NewMessageScorer()

	messages := []*genai.Content{
		genai.NewContentFromText("Hello", genai.RoleUser),
		genai.NewContentFromText("Error: critical bug found", genai.RoleModel),
		genai.NewContentFromText("ok", genai.RoleUser),
		genai.NewContentFromText("done", genai.RoleUser),
		genai.NewContentFromText("I decided to fix the bug", genai.RoleModel),
	}

	scores := s.ScoreMessages(messages)
	selected := s.SelectImportantMessages(messages, scores, 3)

	if len(selected) != 3 {
		t.Fatalf("selected = %d, want 3", len(selected))
	}

	// Should include the error message (critical priority)
	hasError := false
	for _, msg := range selected {
		for _, p := range msg.Parts {
			if p.Text != "" && p.Text == "Error: critical bug found" {
				hasError = true
			}
		}
	}
	if !hasError {
		t.Error("should select critical error message")
	}
}

func TestMessageScorerSelectAllFit(t *testing.T) {
	s := NewMessageScorer()

	messages := []*genai.Content{
		genai.NewContentFromText("Hello", genai.RoleUser),
		genai.NewContentFromText("World", genai.RoleModel),
	}

	scores := s.ScoreMessages(messages)
	selected := s.SelectImportantMessages(messages, scores, 10)

	if len(selected) != 2 {
		t.Error("should return all messages when keepCount > len")
	}
}

func TestMessageScorerCalculateTokenBudget(t *testing.T) {
	s := NewMessageScorer()

	scores := []MessageScore{
		{Priority: PriorityCritical, Score: 1.0},
		{Priority: PriorityLow, Score: 0.2},
	}

	budgets := s.CalculateTokenBudget(scores, 1000)
	if len(budgets) != 2 {
		t.Fatalf("budgets = %d, want 2", len(budgets))
	}

	// Critical should get more budget
	if budgets[0] <= budgets[1] {
		t.Errorf("critical budget %d should be > low budget %d", budgets[0], budgets[1])
	}
}

func TestMessageScorerCalculateTokenBudgetEmpty(t *testing.T) {
	s := NewMessageScorer()
	budgets := s.CalculateTokenBudget(nil, 1000)
	if len(budgets) != 0 {
		t.Error("empty scores should return empty budgets")
	}
}

func TestMessageScorerFileReferences(t *testing.T) {
	s := NewMessageScorer()

	msg := genai.NewContentFromText("I modified internal/app/builder.go and config/config.yaml", genai.RoleModel)
	score := s.ScoreMessage(msg)

	if len(score.References) < 2 {
		t.Errorf("references = %d, should detect file paths", len(score.References))
	}
}

func TestMessageScorerSemanticClientSetup(t *testing.T) {
	s := NewMessageScorer()

	// Initially not enabled
	s.semanticMu.RLock()
	enabled := s.semanticClient != nil
	s.semanticMu.RUnlock()
	if enabled {
		t.Error("semantic should be disabled by default")
	}

	// Setting nil should keep disabled
	s.SetSemanticClient(nil)
	s.semanticMu.RLock()
	enabled = s.semanticClient != nil
	s.semanticMu.RUnlock()
	if enabled {
		t.Error("nil client should not enable semantic scoring")
	}
}

func TestMessageScorerScoreMessagesWithContextNoClient(t *testing.T) {
	s := NewMessageScorer()

	messages := []*genai.Content{
		genai.NewContentFromText("Hello", genai.RoleUser),
		genai.NewContentFromText("World", genai.RoleModel),
	}

	// Without semantic client, should fall back to heuristic
	scores := s.ScoreMessagesWithContext(context.Background(), messages)
	if len(scores) != 2 {
		t.Fatalf("scores = %d, want 2", len(scores))
	}
}

func TestMessageScorerScoreCacheOperations(t *testing.T) {
	s := NewMessageScorer()

	// Cache should start empty
	_, ok := s.getCachedSemanticScore("test-hash")
	if ok {
		t.Error("cache should be empty initially")
	}

	// Set and get
	s.setCachedSemanticScore("test-hash", 0.85)
	score, ok := s.getCachedSemanticScore("test-hash")
	if !ok {
		t.Fatal("should find cached score")
	}
	if score != 0.85 {
		t.Errorf("cached score = %f, want 0.85", score)
	}
}

func TestMessageScorerHashMessage(t *testing.T) {
	msg1 := genai.NewContentFromText("Hello world", genai.RoleUser)
	msg2 := genai.NewContentFromText("Hello world", genai.RoleUser)
	msg3 := genai.NewContentFromText("Different text", genai.RoleUser)

	h1 := hashMessage(msg1)
	h2 := hashMessage(msg2)
	h3 := hashMessage(msg3)

	if h1 != h2 {
		t.Error("same content should produce same hash")
	}
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}
	if h1 == "" {
		t.Error("hash should not be empty")
	}
}

func TestMessageScorerHashMessageWithTools(t *testing.T) {
	msg1 := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "read"}},
		},
	}
	msg2 := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{Name: "write"}},
		},
	}

	h1 := hashMessage(msg1)
	h2 := hashMessage(msg2)
	if h1 == h2 {
		t.Error("different tool calls should produce different hashes")
	}
}

func TestMessageScorerUpdatePriority(t *testing.T) {
	s := NewMessageScorer()

	score := &MessageScore{Score: 0.9, Priority: PriorityNormal}
	s.updatePriority(score)
	if score.Priority != PriorityCritical {
		t.Errorf("score 0.9 priority = %d, want PriorityCritical", score.Priority)
	}

	score = &MessageScore{Score: 0.7, Priority: PriorityNormal}
	s.updatePriority(score)
	if score.Priority != PriorityHigh {
		t.Errorf("score 0.7 priority = %d, want PriorityHigh", score.Priority)
	}

	score = &MessageScore{Score: 0.2, Priority: PriorityNormal}
	s.updatePriority(score)
	if score.Priority != PriorityLow {
		t.Errorf("score 0.2 priority = %d, want PriorityLow", score.Priority)
	}

	score = &MessageScore{Score: 0.5, Priority: PriorityNormal}
	s.updatePriority(score)
	if score.Priority != PriorityNormal {
		t.Errorf("score 0.5 priority = %d, want PriorityNormal (unchanged)", score.Priority)
	}
}

func TestMessageScorerCacheEviction(t *testing.T) {
	s := NewMessageScorer()
	s.scoreCacheTTL = 10 * time.Millisecond

	s.setCachedSemanticScore("key1", 0.5)

	// Should find it immediately
	_, ok := s.getCachedSemanticScore("key1")
	if !ok {
		t.Fatal("should find score before TTL")
	}

	// Wait for expiry
	time.Sleep(20 * time.Millisecond)

	_, ok = s.getCachedSemanticScore("key1")
	if ok {
		t.Error("should not find score after TTL expiry")
	}
}

func TestMessageScorerLargeContent(t *testing.T) {
	s := NewMessageScorer()

	// Large function response should reduce score
	largeContent := make([]byte, 10000)
	for i := range largeContent {
		largeContent[i] = 'a'
	}

	msg := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{
				FunctionResponse: &genai.FunctionResponse{
					Name: "read",
					Response: map[string]any{
						"content": string(largeContent),
						"success": true,
					},
				},
			},
		},
	}

	score := s.ScoreMessage(msg)
	if score.Score >= 0.6 {
		t.Errorf("large content score = %f, should be < 0.6", score.Score)
	}
}
