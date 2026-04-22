package chat

import (
	"testing"

	"google.golang.org/genai"
)

// ─── ReplaceWithSummary ────────────────────────────────────────────────────

func TestReplaceWithSummary_PutsSummaryAtIndexZero(t *testing.T) {
	s := NewSession()
	for range 5 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 10)
	}

	summary := genai.NewContentFromText("summarized", genai.RoleUser)
	s.ReplaceWithSummary(3, summary, 50)

	hist := s.GetHistory()
	if len(hist) == 0 {
		t.Fatal("empty history after replace")
	}
	// Summary must be at index 0 — downstream code (agent.go keepStart=3) relies on this.
	if hist[0] != summary {
		t.Error("summary is not at index 0")
	}
}

func TestReplaceWithSummary_PreservesMessagesAfterBoundary(t *testing.T) {
	s := NewSession()
	tail1 := genai.NewContentFromText("tail1", genai.RoleUser)
	tail2 := genai.NewContentFromText("tail2", genai.RoleModel)

	for range 3 {
		s.AddContentWithTokens(genai.NewContentFromText("old", genai.RoleUser), 10)
	}
	s.AddContentWithTokens(tail1, 5)
	s.AddContentWithTokens(tail2, 7)

	summary := genai.NewContentFromText("s", genai.RoleUser)
	s.ReplaceWithSummary(3, summary, 100)

	hist := s.GetHistory()
	// After: [summary, tail1, tail2]
	if len(hist) != 3 {
		t.Fatalf("len(history) = %d, want 3", len(hist))
	}
	if hist[1] != tail1 || hist[2] != tail2 {
		t.Errorf("tail messages not preserved: %+v", hist)
	}
}

func TestReplaceWithSummary_TokenCountsLengthMatchesHistory(t *testing.T) {
	// CLAUDE.md invariant: len(tokenCounts) == len(History) after every mutation.
	s := NewSession()
	for range 7 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 10)
	}

	summary := genai.NewContentFromText("s", genai.RoleUser)
	s.ReplaceWithSummary(4, summary, 40)

	hist := s.GetHistory()
	counts := s.GetTokenCounts()
	if len(hist) != len(counts) {
		t.Errorf("len(history)=%d, len(tokenCounts)=%d — invariant violated", len(hist), len(counts))
	}
}

func TestReplaceWithSummary_TotalTokensEqualsSumOfCounts(t *testing.T) {
	s := NewSession()
	for range 6 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 10)
	}

	summary := genai.NewContentFromText("s", genai.RoleUser)
	s.ReplaceWithSummary(3, summary, 25)

	counts := s.GetTokenCounts()
	sum := 0
	for _, c := range counts {
		sum += c
	}
	if got := s.GetTokenCount(); got != sum {
		t.Errorf("totalTokens=%d, sum(counts)=%d — must match", got, sum)
	}
}

func TestReplaceWithSummary_SummaryTokensAtIndexZero(t *testing.T) {
	s := NewSession()
	for range 4 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 10)
	}

	summary := genai.NewContentFromText("s", genai.RoleUser)
	s.ReplaceWithSummary(2, summary, 99)

	counts := s.GetTokenCounts()
	if len(counts) == 0 || counts[0] != 99 {
		t.Errorf("tokenCounts[0] = %v, want 99", counts)
	}
}

func TestReplaceWithSummary_UpToIndexPastEndIsClamped(t *testing.T) {
	// upToIndex > len(History) must be clamped to len(History) instead of panicking.
	s := NewSession()
	for range 3 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 10)
	}

	summary := genai.NewContentFromText("s", genai.RoleUser)
	s.ReplaceWithSummary(99, summary, 5)

	hist := s.GetHistory()
	// All messages replaced → [summary] alone.
	if len(hist) != 1 {
		t.Errorf("len(hist) = %d, want 1 (only summary)", len(hist))
	}
	if len(s.GetTokenCounts()) != 1 {
		t.Error("tokenCounts must match history length = 1")
	}
}

func TestReplaceWithSummary_MissingTokenCountsPadWithZero(t *testing.T) {
	// If tokenCounts is shorter than History (e.g. when AddContent was used
	// without tokens), the replace must still produce len-aligned tokenCounts.
	s := NewSession()
	// Add two messages WITHOUT tokens via AddContent (doesn't extend tokenCounts).
	s.AddContent(genai.NewContentFromText("a", genai.RoleUser))
	s.AddContent(genai.NewContentFromText("b", genai.RoleUser))
	// Add two with tokens.
	s.AddContentWithTokens(genai.NewContentFromText("c", genai.RoleUser), 10)
	s.AddContentWithTokens(genai.NewContentFromText("d", genai.RoleUser), 10)

	summary := genai.NewContentFromText("s", genai.RoleUser)
	s.ReplaceWithSummary(2, summary, 5)

	hist := s.GetHistory()
	counts := s.GetTokenCounts()
	if len(hist) != len(counts) {
		t.Fatalf("len(hist)=%d, len(counts)=%d — must match", len(hist), len(counts))
	}
	// After: [summary(5), c(10), d(10)] = len 3
	if len(hist) != 3 {
		t.Errorf("len(hist) = %d, want 3", len(hist))
	}
}

// ─── TrimHistory ───────────────────────────────────────────────────────────

func TestTrimHistory_TokenCountsLengthMatchesHistory(t *testing.T) {
	s := NewSession()
	// Exceed MaxMessages with accompanying token counts.
	for range MaxMessages + 20 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 3)
	}

	s.TrimHistory()

	hist := s.GetHistory()
	counts := s.GetTokenCounts()
	if len(hist) != len(counts) {
		t.Errorf("post-trim: len(hist)=%d, len(counts)=%d — invariant violated", len(hist), len(counts))
	}
	if len(hist) > MaxMessages {
		t.Errorf("post-trim len(hist)=%d exceeds MaxMessages=%d", len(hist), MaxMessages)
	}
}

func TestTrimHistory_TotalTokensRecalculated(t *testing.T) {
	s := NewSession()
	for range MaxMessages + 10 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 7)
	}

	s.TrimHistory()

	counts := s.GetTokenCounts()
	sum := 0
	for _, c := range counts {
		sum += c
	}
	if s.GetTokenCount() != sum {
		t.Errorf("totalTokens=%d, sum(counts)=%d", s.GetTokenCount(), sum)
	}
}

func TestTrimHistory_NoOpBelowMaxMessages(t *testing.T) {
	s := NewSession()
	for range MaxMessages / 2 {
		s.AddContentWithTokens(genai.NewContentFromText("m", genai.RoleUser), 1)
	}
	before := len(s.GetHistory())

	s.TrimHistory()

	if got := len(s.GetHistory()); got != before {
		t.Errorf("TrimHistory should be a no-op below MaxMessages; len went %d → %d", before, got)
	}
}

func TestTrimHistory_MissingTokenCountsPadWithZero(t *testing.T) {
	// Add messages via AddContent (no tokens) alongside AddContentWithTokens.
	// After trim, tokenCounts must still align with history.
	s := NewSession()
	for i := range MaxMessages + 5 {
		if i%2 == 0 {
			s.AddContent(genai.NewContentFromText("no-tok", genai.RoleUser))
		} else {
			s.AddContentWithTokens(genai.NewContentFromText("with-tok", genai.RoleUser), 4)
		}
	}

	s.TrimHistory()

	if len(s.GetHistory()) != len(s.GetTokenCounts()) {
		t.Error("trim must produce aligned len")
	}
}
