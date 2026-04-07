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
			// All providers report cumulative totals, not per-chunk deltas.
			if chunk.InputTokens > 0 {
				resp.InputTokens = chunk.InputTokens
			}
			if chunk.OutputTokens > 0 {
				resp.OutputTokens = chunk.OutputTokens
			}
			if handler.OnTokenUpdate != nil && (chunk.InputTokens > 0 || chunk.OutputTokens > 0) {
				handler.OnTokenUpdate(resp.InputTokens, resp.OutputTokens)
			}
			if chunk.CacheReadInputTokens > 0 {
				resp.CacheReadInputTokens = chunk.CacheReadInputTokens
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
