package context

import (
	"context"
	"testing"

	"gokin/internal/testkit"

	"google.golang.org/genai"
)

// TestDoSummarize_RejectsEmptyOutput pins the data-loss guard: a blank/degenerate
// LLM summary must NOT be accepted (it would replace the summarized conversation
// middle with nothing). doSummarize must return an error so the caller falls back
// to truncation.
func TestDoSummarize_RejectsEmptyOutput(t *testing.T) {
	msgs := []*genai.Content{genai.NewContentFromText("some prior conversation", genai.RoleUser)}

	for _, out := range []string{"", "   ", "\n\n", "N/A"} {
		mc := testkit.NewMockClient()
		mc.EnqueueText(out)
		s := NewSummarizer(mc)
		if _, err := s.doSummarize(context.Background(), msgs); err == nil {
			t.Errorf("doSummarize must reject degenerate output %q (silent data loss otherwise)", out)
		}
	}
}

// TestDoSummarize_AcceptsRealSummary guards against over-blocking: a genuine
// summary is accepted and wrapped.
func TestDoSummarize_AcceptsRealSummary(t *testing.T) {
	mc := testkit.NewMockClient()
	mc.EnqueueText("User implemented the backup feature; touched internal/cmd/backup.go and added tests.")
	s := NewSummarizer(mc)
	msgs := []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}

	got, err := s.doSummarize(context.Background(), msgs)
	if err != nil {
		t.Fatalf("a real summary should succeed: %v", err)
	}
	if got == nil || len(got.Parts) == 0 {
		t.Fatal("expected a non-empty summary content")
	}
}
