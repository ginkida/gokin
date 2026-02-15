package client

import (
	"gokin/internal/logging"

	"google.golang.org/genai"
)

// sanitizeToolPairs removes orphaned FunctionCall/FunctionResponse parts from history.
// A FunctionCall without a matching FunctionResponse (or vice versa) causes API errors
// such as MiniMax 400 "tool result's tool id not found". This is a last-defense sanitizer
// called before history conversion in the Anthropic client.
func sanitizeToolPairs(history []*genai.Content) []*genai.Content {
	return sanitizeToolPairsWithPendingResults(history, nil)
}

// sanitizeToolPairsWithPendingResults is like sanitizeToolPairs, but treats the provided
// pending function results as if they were already present in history.
// This is required in SendFunctionResponse paths where tool_result is carried separately
// and not yet appended to history at sanitization time.
func sanitizeToolPairsWithPendingResults(history []*genai.Content, pendingResults []*genai.FunctionResponse) []*genai.Content {
	if len(history) == 0 {
		return history
	}

	// Pass 1: collect all FunctionCall IDs and FunctionResponse IDs
	callIDs := make(map[string]bool)
	responseIDs := make(map[string]bool)
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				callIDs[part.FunctionCall.ID] = true
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responseIDs[part.FunctionResponse.ID] = true
			}
		}
	}
	for _, result := range pendingResults {
		if result != nil && result.ID != "" {
			responseIDs[result.ID] = true
		}
	}

	// Pass 2: count orphans (quick check to avoid unnecessary allocations)
	orphanedCalls := 0
	orphanedResponses := 0
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				if !responseIDs[part.FunctionCall.ID] {
					orphanedCalls++
				}
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				if !callIDs[part.FunctionResponse.ID] {
					orphanedResponses++
				}
			}
		}
	}

	// Fast path: no orphans found, return original history unmodified
	if orphanedCalls == 0 && orphanedResponses == 0 {
		return history
	}

	logging.Warn("sanitizeToolPairs removing orphaned parts",
		"orphaned_calls", orphanedCalls,
		"orphaned_responses", orphanedResponses)

	// Pass 3: build cleaned history, removing orphaned parts.
	// IMPORTANT: create new Content objects — never mutate originals, which are
	// shared with Session via shallow-copied slice from GetHistory().
	result := make([]*genai.Content, 0, len(history))
	for _, msg := range history {
		if msg == nil {
			continue
		}

		keptParts := make([]*genai.Part, 0, len(msg.Parts))
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}

			keep := true
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				if !responseIDs[part.FunctionCall.ID] {
					keep = false
				}
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				if !callIDs[part.FunctionResponse.ID] {
					keep = false
				}
			}

			if keep {
				keptParts = append(keptParts, part)
			}
		}

		// Drop messages that became empty after cleanup
		if len(keptParts) > 0 {
			cleaned := &genai.Content{
				Role:  msg.Role,
				Parts: keptParts,
			}
			result = append(result, cleaned)
		}
	}

	logging.Warn("sanitizeToolPairs completed",
		"history_before", len(history),
		"history_after", len(result))

	return result
}
