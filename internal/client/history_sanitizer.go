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
//
// Uses counts (not booleans) so that duplicate tool_use_ids across
// messages are handled correctly. Example failure mode: the same
// `memory:55` id appears in two assistant messages (e.g. after a
// retry-driven history merge). With a bool map, one response was
// interpreted as pairing both calls, and the provider rejected with
// "N tool_call_ids did not have response messages". With counts, the
// second occurrence of a call without an extra response becomes orphan
// and gets dropped.
func sanitizeToolPairsWithPendingResults(history []*genai.Content, pendingResults []*genai.FunctionResponse) []*genai.Content {
	if len(history) == 0 {
		return history
	}

	// Pass 1: count occurrences of each FunctionCall ID and FunctionResponse ID.
	callCounts := make(map[string]int)
	responseCounts := make(map[string]int)
	for _, msg := range history {
		if msg == nil {
			continue
		}
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				callCounts[part.FunctionCall.ID]++
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				responseCounts[part.FunctionResponse.ID]++
			}
		}
	}
	for _, result := range pendingResults {
		if result != nil && result.ID != "" {
			responseCounts[result.ID]++
		}
	}

	// Pass 2: count orphans (quick check to avoid unnecessary allocations)
	orphanedCalls := 0
	orphanedResponses := 0
	for id, c := range callCounts {
		if r := responseCounts[id]; c > r {
			orphanedCalls += c - r
		}
	}
	for id, r := range responseCounts {
		if c := callCounts[id]; r > c {
			orphanedResponses += r - c
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
	// Track remaining-budget per id as we traverse: the first K
	// occurrences of each call/response (where K = min(callCount,
	// responseCount)) are kept; extras are dropped. This matches the
	// API's position-based pairing (each call_id on an assistant turn
	// wants exactly one response on the next user turn).
	//
	// IMPORTANT: create new Content objects — never mutate originals,
	// which are shared with Session via shallow-copied slice from
	// GetHistory().
	callBudget := make(map[string]int, len(callCounts))
	responseBudget := make(map[string]int, len(responseCounts))
	for id, c := range callCounts {
		r := responseCounts[id]
		if c < r {
			callBudget[id] = c
			responseBudget[id] = c
		} else {
			callBudget[id] = r
			responseBudget[id] = r
		}
	}

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
				id := part.FunctionCall.ID
				if callBudget[id] > 0 {
					callBudget[id]--
				} else {
					keep = false
				}
			}
			if part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
				id := part.FunctionResponse.ID
				if responseBudget[id] > 0 {
					responseBudget[id]--
				} else {
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
