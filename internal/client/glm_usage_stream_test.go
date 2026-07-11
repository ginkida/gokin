package client

import (
	"testing"

	"google.golang.org/genai"
)

// TestGLMUsageEventsPreserveTokenAndImplicitCacheMetrics pins the Z.AI
// Anthropic-compatible usage shape. message_start carries prompt/cache usage;
// message_delta carries generated output usage and the stop reason.
func TestGLMUsageEventsPreserveTokenAndImplicitCacheMetrics(t *testing.T) {
	c := &AnthropicClient{}
	acc := &toolCallAccumulator{}

	start := c.processStreamEvent(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"usage": map[string]any{
				"input_tokens":                float64(12_345),
				"cache_creation_input_tokens": float64(321),
				"cache_read_input_tokens":     float64(10_000),
			},
		},
	}, acc)
	if start.InputTokens != 12_345 || start.CacheCreationInputTokens != 321 || start.CacheReadInputTokens != 10_000 {
		t.Fatalf("message_start usage = input %d creation %d read %d",
			start.InputTokens, start.CacheCreationInputTokens, start.CacheReadInputTokens)
	}

	delta := c.processStreamEvent(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn"},
		"usage": map[string]any{"output_tokens": float64(777)},
	}, acc)
	if delta.OutputTokens != 777 {
		t.Fatalf("message_delta output tokens = %d, want 777", delta.OutputTokens)
	}
	if !delta.Done || delta.FinishReason != genai.FinishReasonStop {
		t.Fatalf("message_delta completion = done %v reason %v", delta.Done, delta.FinishReason)
	}
}
