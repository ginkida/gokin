package context

import (
	"testing"

	"google.golang.org/genai"
)

// TestScoreMessagesNilSafe pins the v0.85.13 fix: a nil entry in the message
// slice must not panic the scorer (message_scorer now guards nil like
// relevance_scorer does at every site).
func TestScoreMessagesNilSafe(t *testing.T) {
	s := NewMessageScorer()
	msgs := []*genai.Content{
		nil,
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
		nil,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ScoreMessages panicked on nil entry: %v", r)
		}
	}()

	scores := s.ScoreMessages(msgs)
	if len(scores) != len(msgs) {
		t.Fatalf("got %d scores, want %d", len(scores), len(msgs))
	}
	if scores[0].Priority != PriorityLow {
		t.Errorf("nil message priority = %v, want PriorityLow (droppable)", scores[0].Priority)
	}

	// hashMessage must also be nil-safe (used as the semantic-cache key).
	if got := hashMessage(nil); got == "" {
		t.Error("hashMessage(nil) returned empty string, want a stable sentinel")
	}
}

// TestGetUsageZeroLimitNoInf pins the v0.85.13 fix: a zero/unset MaxInputTokens
// must not produce +Inf PercentUsed (which would falsely report NearLimit).
func TestGetUsageZeroLimitNoInf(t *testing.T) {
	tc := &TokenCounter{} // zero-value limits: MaxInputTokens == 0
	u := tc.GetUsage(1000)
	if u.PercentUsed != 0 {
		t.Errorf("PercentUsed = %v with zero limit, want 0 (no Inf)", u.PercentUsed)
	}
	if u.NearLimit {
		t.Error("NearLimit = true with zero limit; Inf comparison leaked through")
	}
}
