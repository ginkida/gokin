package client

import (
	"testing"

	"google.golang.org/genai"
)

// Regression: before v0.70.4, sanitizer used map[string]bool so if the
// SAME tool_use_id (e.g. "memory:55") appeared in two assistant
// messages, one response was interpreted as pairing both — API then
// rejected with "N tool_call_ids did not have response messages".
// With count-based sanitization, the second call occurrence is
// correctly recognised as orphan.
func TestSanitizeToolPairs_DuplicateIDOneResponse(t *testing.T) {
	history := []*genai.Content{
		// First model turn with memory:55
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "memory:55", Name: "memory"}},
			},
		},
		// Its response
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "memory:55", Name: "memory"}},
			},
		},
		// Second model turn with the SAME id (retry merge / server reused id)
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "memory:55", Name: "memory"}},
			},
		},
		// NO response for the second occurrence
	}

	cleaned := sanitizeToolPairs(history)

	// The second model turn's call is the orphan. It should be dropped.
	var callCount, responseCount int
	for _, msg := range cleaned {
		for _, p := range msg.Parts {
			if p.FunctionCall != nil && p.FunctionCall.ID == "memory:55" {
				callCount++
			}
			if p.FunctionResponse != nil && p.FunctionResponse.ID == "memory:55" {
				responseCount++
			}
		}
	}
	if callCount != responseCount {
		t.Errorf("after sanitize: %d calls vs %d responses (want equal — otherwise API rejects)",
			callCount, responseCount)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 call surviving (the one with response), got %d", callCount)
	}
}

// Mirror case: two responses, one call. Second response is orphan.
func TestSanitizeToolPairs_DuplicateIDOneCall(t *testing.T) {
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "x:1", Name: "x"}},
			},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "x:1", Name: "x"}},
				{FunctionResponse: &genai.FunctionResponse{ID: "x:1", Name: "x"}}, // duplicate, orphan
			},
		},
	}

	cleaned := sanitizeToolPairs(history)

	var callCount, responseCount int
	for _, msg := range cleaned {
		for _, p := range msg.Parts {
			if p.FunctionCall != nil {
				callCount++
			}
			if p.FunctionResponse != nil {
				responseCount++
			}
		}
	}
	if callCount != responseCount {
		t.Errorf("calls=%d responses=%d, want equal", callCount, responseCount)
	}
}

// Existing behaviour — single properly-paired tool_use shouldn't
// trigger any orphan logic or allocation.
func TestSanitizeToolPairs_CleanHistoryUnchanged(t *testing.T) {
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "read:1", Name: "read"}},
			},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "read:1", Name: "read"}},
			},
		},
	}
	cleaned := sanitizeToolPairs(history)
	if len(cleaned) != 2 {
		t.Errorf("clean history should survive unchanged; got len=%d", len(cleaned))
	}
}

// Multiple DIFFERENT ids, each properly paired — counts still balance.
func TestSanitizeToolPairs_MultipleDistinctPairs(t *testing.T) {
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "a:1", Name: "a"}},
				{FunctionCall: &genai.FunctionCall{ID: "b:1", Name: "b"}},
				{FunctionCall: &genai.FunctionCall{ID: "c:1", Name: "c"}},
			},
		},
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "a:1"}},
				{FunctionResponse: &genai.FunctionResponse{ID: "b:1"}},
				{FunctionResponse: &genai.FunctionResponse{ID: "c:1"}},
			},
		},
	}
	cleaned := sanitizeToolPairs(history)
	if len(cleaned) != 2 || len(cleaned[0].Parts) != 3 || len(cleaned[1].Parts) != 3 {
		t.Errorf("3-distinct-pair history should survive intact; got %+v", cleaned)
	}
}

// Pending-results path: responses from the current SendFunctionResponse
// call aren't yet in history. Sanitizer must count them too.
func TestSanitizeToolPairs_PendingResultsRegistered(t *testing.T) {
	history := []*genai.Content{
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "pending:1", Name: "x"}},
			},
		},
	}
	pending := []*genai.FunctionResponse{
		{ID: "pending:1", Name: "x"},
	}
	cleaned := sanitizeToolPairsWithPendingResults(history, pending)
	// Call should NOT be orphaned because pending result counts as response.
	var callCount int
	for _, msg := range cleaned {
		for _, p := range msg.Parts {
			if p.FunctionCall != nil {
				callCount++
			}
		}
	}
	if callCount != 1 {
		t.Errorf("pending-result path must keep paired call; got %d calls", callCount)
	}
}
