package client

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// The context bar accuracy fix (v0.100.90): caching providers report
// input_tokens EXCLUSIVE of cache reads — on a warm prefix cache it collapses
// to the uncached tail (the documented glm probe: 2848 → 32 with
// cache_read=2816). OnTokenUpdate feeds the live context bar and
// ContextManager.ObserveAPIUsage (which adopts the number as authoritative,
// IsEstimate=false), so passing the billed remainder alone made the display
// claim a context ~100× smaller than reality mid-turn. The callback must
// receive the FULL prompt-side size: input + cache_read + cache_creation.
func TestProcessStream_OnTokenUpdateIncludesCachedPromptTokens(t *testing.T) {
	sr := buildStream([]ResponseChunk{
		// message_start delivers all three usage fields in ONE chunk —
		// exactly how anthropic.go's SSE parser emits them.
		{InputTokens: 32, CacheReadInputTokens: 55000, CacheCreationInputTokens: 1000},
		{Text: "hello"},
		{OutputTokens: 12, Done: true, FinishReason: genai.FinishReasonStop},
	})

	var gotInput, gotOutput int
	handler := &StreamHandler{
		OnTokenUpdate: func(inputTokens, outputTokens int) {
			gotInput, gotOutput = inputTokens, outputTokens
		},
	}
	resp, err := ProcessStream(context.Background(), sr, handler)
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if gotInput != 32+55000+1000 {
		t.Fatalf("OnTokenUpdate inputTokens = %d, want %d (full prompt incl. cache read/creation)",
			gotInput, 32+55000+1000)
	}
	if gotOutput != 12 {
		t.Fatalf("OnTokenUpdate outputTokens = %d, want 12", gotOutput)
	}
	// Billing fields stay untouched: resp keeps the raw provider split.
	if resp.InputTokens != 32 || resp.CacheReadInputTokens != 55000 || resp.CacheCreationInputTokens != 1000 {
		t.Fatalf("resp usage split = input %d / cacheRead %d / cacheCreate %d, want raw 32/55000/1000",
			resp.InputTokens, resp.CacheReadInputTokens, resp.CacheCreationInputTokens)
	}
}
