package client

import (
	"context"

	"google.golang.org/genai"
)

// StreamHandler provides callbacks for handling streaming responses.
type StreamHandler struct {
	// OnText is called for each text chunk received.
	OnText func(text string)

	// OnThinking is called for each thinking/reasoning chunk received.
	OnThinking func(text string)

	// OnFunctionCall is called for each function call received.
	OnFunctionCall func(fc *genai.FunctionCall)

	// OnError is called when an error occurs.
	OnError func(err error)

	// OnTokenUpdate is called when the stream provides token usage metadata.
	// inputTokens is the FULL prompt-side context (input_tokens +
	// cache_read_input_tokens + cache_creation_input_tokens) — the number a
	// context-usage display needs — NOT the cache-exclusive billed input.
	OnTokenUpdate func(inputTokens, outputTokens int)

	// OnRateLimit is called when the stream provides rate limit metadata.
	OnRateLimit func(rl *RateLimitMetadata)

	// OnComplete is called when the response is complete.
	OnComplete func(response *Response)
}

// ProcessStream processes a streaming response with the given handler.
func ProcessStream(ctx context.Context, sr *StreamingResponse, handler *StreamHandler) (*Response, error) {
	resp := &Response{}

	for {
		select {
		case <-ctx.Done():
			return nil, ContextErr(ctx)
		case chunk, ok := <-sr.Chunks:
			if !ok {
				// A producer may close its channel while cancellation is racing
				// with delivery of the terminal error chunk. Cancellation must not
				// be reclassified as a successful (possibly empty) response merely
				// because the closed channel won the select.
				if err := ContextErr(ctx); err != nil {
					return nil, err
				}
				if handler.OnComplete != nil {
					handler.OnComplete(resp)
				}
				return resp, nil
			}

			if chunk.Error != nil {
				if handler.OnError != nil {
					handler.OnError(chunk.Error)
				}
				// Return accumulated partial response alongside the error
				// so callers can preserve context from partial model output
				return resp, chunk.Error
			}

			if chunk.Thinking != "" {
				resp.Thinking += chunk.Thinking
				if handler.OnThinking != nil {
					handler.OnThinking(chunk.Thinking)
				}
			}

			if chunk.Text != "" {
				resp.Text += chunk.Text
				if handler.OnText != nil {
					handler.OnText(chunk.Text)
				}
			}

			// Accumulate original Parts (preserves ThoughtSignature for Gemini 3).
			// Track which FunctionCalls came with original parts to avoid duplicates.
			fcInParts := make(map[*genai.FunctionCall]bool)
			for _, part := range chunk.Parts {
				if part != nil {
					resp.Parts = append(resp.Parts, part)
					if part.FunctionCall != nil {
						fcInParts[part.FunctionCall] = true
					}
				}
			}

			for _, fc := range chunk.FunctionCalls {
				resp.FunctionCalls = append(resp.FunctionCalls, fc)
				// CRITICAL: Also add to Parts so history reconstruction works correctly
				// Only add if not already added from original chunk.Parts!
				if !fcInParts[fc] {
					resp.Parts = append(resp.Parts, &genai.Part{FunctionCall: fc})
				}
				if handler.OnFunctionCall != nil {
					handler.OnFunctionCall(fc)
				}
			}

			// Keep the latest non-zero usage metadata (typically from the final chunk).
			// All providers report cumulative totals, not per-chunk deltas. Cache
			// fields must be captured BEFORE the OnTokenUpdate callback below —
			// message_start delivers input_tokens and cache_read_input_tokens in
			// the SAME chunk, and the callback needs both.
			if chunk.InputTokens > 0 {
				resp.InputTokens = chunk.InputTokens
			}
			if chunk.OutputTokens > 0 {
				resp.OutputTokens = chunk.OutputTokens
			}
			if chunk.CacheReadInputTokens > 0 {
				resp.CacheReadInputTokens = chunk.CacheReadInputTokens
			}
			if chunk.CacheCreationInputTokens > 0 {
				// Mirror the sibling StreamingResponse.Collect() (client.go): without
				// this, cache-creation token accounting on the executor + agent
				// stream paths stays permanently 0.
				resp.CacheCreationInputTokens = chunk.CacheCreationInputTokens
			}
			if handler.OnTokenUpdate != nil && (chunk.InputTokens > 0 || chunk.OutputTokens > 0) {
				// Report the FULL prompt-side context, not the billed remainder:
				// caching providers (glm/deepseek/kimi/anthropic) report
				// input_tokens EXCLUSIVE of cache_read/cache_creation — on a warm
				// prefix cache input_tokens collapses to the uncached tail (the
				// documented glm probe: 2848 → 32 with cache_read=2816), so
				// passing it alone made the live context bar and
				// ContextManager.ObserveAPIUsage authoritatively adopt a number
				// ~100× smaller than the real context mid-turn (v0.100.90).
				// Billing paths deliberately do NOT use this callback — they read
				// resp.InputTokens/CacheReadInputTokens directly.
				handler.OnTokenUpdate(
					resp.InputTokens+resp.CacheReadInputTokens+resp.CacheCreationInputTokens,
					resp.OutputTokens)
			}
			if chunk.RateLimit != nil {
				resp.RateLimit = chunk.RateLimit
				if handler.OnRateLimit != nil {
					handler.OnRateLimit(chunk.RateLimit)
				}
			}

			if chunk.Done {
				resp.FinishReason = chunk.FinishReason
				if handler.OnComplete != nil {
					handler.OnComplete(resp)
				}
				return resp, nil
			}
		}
	}
}

// CollectText is a convenience function that collects only text from a stream.
func CollectText(ctx context.Context, sr *StreamingResponse) (string, error) {
	resp, err := ProcessStream(ctx, sr, &StreamHandler{})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}
