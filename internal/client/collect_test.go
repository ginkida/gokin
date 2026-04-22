package client

import (
	"errors"
	"testing"

	"google.golang.org/genai"
)

// buildStream is a test helper that wraps a slice of chunks into a
// *StreamingResponse with closed channels, ready for Collect() to consume.
func buildStream(chunks []ResponseChunk) *StreamingResponse {
	ch := make(chan ResponseChunk, len(chunks))
	done := make(chan struct{})
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	close(done)
	return &StreamingResponse{Chunks: ch, Done: done}
}

func TestCollect_AccumulatesText(t *testing.T) {
	sr := buildStream([]ResponseChunk{
		{Text: "hello "},
		{Text: "world"},
		{Done: true, FinishReason: genai.FinishReasonStop},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.Text != "hello world" {
		t.Errorf("Text = %q, want %q", resp.Text, "hello world")
	}
	if resp.FinishReason != genai.FinishReasonStop {
		t.Errorf("FinishReason = %v", resp.FinishReason)
	}
}

func TestCollect_AccumulatesThinking(t *testing.T) {
	sr := buildStream([]ResponseChunk{
		{Thinking: "let me think..."},
		{Thinking: " and more."},
		{Text: "answer"},
		{Done: true},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.Thinking != "let me think... and more." {
		t.Errorf("Thinking = %q", resp.Thinking)
	}
	if resp.Text != "answer" {
		t.Errorf("Text = %q", resp.Text)
	}
}

func TestCollect_TakesLatestNonZeroInputTokens(t *testing.T) {
	// Providers send CUMULATIVE totals, not deltas. Later non-zero value wins
	// via assignment (= not +=). The final chunk typically carries the final count.
	sr := buildStream([]ResponseChunk{
		{Text: "a", InputTokens: 50},
		{Text: "b", InputTokens: 100},
		{Done: true, InputTokens: 150},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.InputTokens != 150 {
		t.Errorf("InputTokens = %d, want 150 (final cumulative, not 50+100+150)", resp.InputTokens)
	}
}

func TestCollect_ZeroDoesNotOverwriteEarlier(t *testing.T) {
	// If later chunks have zero usage (some providers only report once),
	// we keep the last non-zero value.
	sr := buildStream([]ResponseChunk{
		{InputTokens: 100, OutputTokens: 50},
		{Text: "partial"},
		{Done: true}, // zero tokens on final chunk
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100 (preserved)", resp.InputTokens)
	}
	if resp.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50 (preserved)", resp.OutputTokens)
	}
}

func TestCollect_CacheTokensTrackedIndependently(t *testing.T) {
	sr := buildStream([]ResponseChunk{
		{InputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 80},
		{Done: true, InputTokens: 100, CacheCreationInputTokens: 20, CacheReadInputTokens: 80},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.CacheCreationInputTokens != 20 {
		t.Errorf("CacheCreation = %d, want 20", resp.CacheCreationInputTokens)
	}
	if resp.CacheReadInputTokens != 80 {
		t.Errorf("CacheRead = %d, want 80", resp.CacheReadInputTokens)
	}
}

func TestCollect_CacheTokenLastNonZeroWins(t *testing.T) {
	// Anthropic-compat sometimes emits cache counts only once — make sure
	// a subsequent zero doesn't clobber.
	sr := buildStream([]ResponseChunk{
		{CacheCreationInputTokens: 50},
		{Text: "x"},
		{Done: true}, // zero cache tokens
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.CacheCreationInputTokens != 50 {
		t.Errorf("CacheCreation = %d, want 50 (preserved)", resp.CacheCreationInputTokens)
	}
}

func TestCollect_RateLimitLatestWins(t *testing.T) {
	rl1 := &RateLimitMetadata{RequestsRemaining: 100, TokensRemaining: 10000}
	rl2 := &RateLimitMetadata{RequestsRemaining: 99, TokensRemaining: 9500}

	sr := buildStream([]ResponseChunk{
		{RateLimit: rl1},
		{Text: "x", RateLimit: rl2},
		{Done: true},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.RateLimit == nil {
		t.Fatal("RateLimit is nil")
	}
	if resp.RateLimit.RequestsRemaining != 99 {
		t.Errorf("RequestsRemaining = %d, want 99 (latest)", resp.RateLimit.RequestsRemaining)
	}
}

func TestCollect_BackfillsPartsFromFunctionCalls(t *testing.T) {
	// Regression: Collect() used to only accumulate chunk.FunctionCalls
	// without back-filling Response.Parts. Downstream callers that use
	// Parts to reconstruct history for SendFunctionResponse then lost
	// tool_use IDs and the provider rejected the follow-up with
	// "tool_use_id not found". Collect now mirrors ProcessStream's
	// back-fill so Parts stays populated.
	fc := &genai.FunctionCall{Name: "get_weather", Args: map[string]any{"city": "Oslo"}}
	sr := buildStream([]ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{fc}},
		{Done: true, FinishReason: genai.FinishReasonStop},
	})
	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.FunctionCalls) != 1 {
		t.Fatalf("FunctionCalls = %d, want 1", len(resp.FunctionCalls))
	}
	if len(resp.Parts) != 1 {
		t.Fatalf("Parts = %d, want 1 (back-filled)", len(resp.Parts))
	}
	if resp.Parts[0].FunctionCall != fc {
		t.Errorf("Parts[0].FunctionCall mismatch — expected the same pointer back-filled")
	}
}

func TestCollect_DoesNotDuplicateWhenChunkAlreadyHasParts(t *testing.T) {
	// If a chunk emits FunctionCall both through Parts (for Gemini 3's
	// ThoughtSignature preservation) AND through FunctionCalls (for
	// Anthropic-compat back-fill), Collect must de-dupe so the same
	// FunctionCall isn't in Parts twice.
	fc := &genai.FunctionCall{Name: "read", Args: map[string]any{"path": "/x"}}
	part := &genai.Part{FunctionCall: fc}
	sr := buildStream([]ResponseChunk{
		{
			Parts:         []*genai.Part{part},
			FunctionCalls: []*genai.FunctionCall{fc},
		},
		{Done: true},
	})
	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Parts) != 1 {
		t.Errorf("Parts = %d, want 1 (de-duped)", len(resp.Parts))
	}
}

func TestCollect_AccumulatesFunctionCalls(t *testing.T) {
	fc1 := &genai.FunctionCall{Name: "read", Args: map[string]any{"path": "/a"}}
	fc2 := &genai.FunctionCall{Name: "write", Args: map[string]any{"path": "/b"}}

	sr := buildStream([]ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{fc1}},
		{FunctionCalls: []*genai.FunctionCall{fc2}},
		{Done: true, FinishReason: genai.FinishReasonStop},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.FunctionCalls) != 2 {
		t.Fatalf("FunctionCalls len = %d, want 2", len(resp.FunctionCalls))
	}
	if resp.FunctionCalls[0].Name != "read" || resp.FunctionCalls[1].Name != "write" {
		t.Errorf("got %v, want [read write]", resp.FunctionCalls)
	}
}

func TestCollect_ReturnsErrorWithPartialResponse(t *testing.T) {
	want := errors.New("mid-stream server error")
	sr := buildStream([]ResponseChunk{
		{Text: "partial "},
		{Text: "output"},
		{Error: want, Done: true},
	})

	resp, err := sr.Collect()
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrap of %v", err, want)
	}
	// Partial text must still be available — retry logic depends on this.
	if resp.Text != "partial output" {
		t.Errorf("partial Text = %q, want %q", resp.Text, "partial output")
	}
}

func TestCollect_ErrorEarlyShortCircuits(t *testing.T) {
	// If error comes on the very first chunk, we still get a non-nil response
	// (zero-valued) and the error.
	want := errors.New("instant failure")
	sr := buildStream([]ResponseChunk{
		{Error: want},
	})

	resp, err := sr.Collect()
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	if resp.Text != "" {
		t.Errorf("Text = %q, want empty", resp.Text)
	}
}

func TestCollect_EmptyStreamNoError(t *testing.T) {
	sr := buildStream([]ResponseChunk{})

	resp, err := sr.Collect()
	if err != nil {
		t.Errorf("empty stream err = %v", err)
	}
	if resp == nil {
		t.Fatal("response must not be nil")
	}
	if resp.Text != "" || len(resp.FunctionCalls) != 0 || resp.InputTokens != 0 {
		t.Errorf("empty stream should produce zero-valued response, got %+v", resp)
	}
}

func TestCollect_FinishReasonFromDoneChunk(t *testing.T) {
	sr := buildStream([]ResponseChunk{
		{Text: "x"},
		// Intermediate chunk without Done — FinishReason should be ignored.
		{FinishReason: genai.FinishReasonOther},
		{Done: true, FinishReason: genai.FinishReasonStop},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if resp.FinishReason != genai.FinishReasonStop {
		t.Errorf("FinishReason = %v, want Stop (only Done chunk counts)", resp.FinishReason)
	}
}

func TestCollect_AccumulatesParts(t *testing.T) {
	// Parts carry ThoughtSignature and other genai-specific metadata —
	// Collect must preserve them across chunks.
	p1 := &genai.Part{Text: "first"}
	p2 := &genai.Part{Text: "second"}

	sr := buildStream([]ResponseChunk{
		{Parts: []*genai.Part{p1}},
		{Parts: []*genai.Part{p2}},
		{Done: true},
	})

	resp, err := sr.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Parts) != 2 {
		t.Fatalf("Parts len = %d, want 2", len(resp.Parts))
	}
	if resp.Parts[0] != p1 || resp.Parts[1] != p2 {
		t.Errorf("Parts not in order")
	}
}
