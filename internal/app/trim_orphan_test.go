package app

import (
	"testing"

	"google.golang.org/genai"
)

// Regression: before v0.70.4, trimToLastModelMessage preserved the
// last model turn verbatim, including any FunctionCall parts whose
// tool results never made it into history (e.g. after an exhausted
// retry on SendFunctionResponse). The orphan call got persisted in
// session.History, every subsequent turn's API call would then fail
// with "N tool_call_ids did not have response messages". Users had
// to /clear to recover.
//
// Fix: trimToLastModelMessage now calls stripOrphanFunctionCalls on
// the kept slice to remove any tool_call part lacking a paired
// response anywhere in history.
func TestStripOrphanFunctionCalls_DropsOrphanFromModelTurn(t *testing.T) {
	history := []*genai.Content{
		// Model turn: one call gets a response, one doesn't.
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "read:1", Name: "read"}},
				{FunctionCall: &genai.FunctionCall{ID: "memory:55", Name: "memory"}}, // orphan
			},
		},
		// Only read:1 has a response.
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{ID: "read:1", Name: "read"}},
			},
		},
	}
	cleaned := stripOrphanFunctionCalls(history)

	// Expect only read:1 to survive; memory:55 must be gone.
	var calls []string
	for _, msg := range cleaned {
		for _, p := range msg.Parts {
			if p.FunctionCall != nil {
				calls = append(calls, p.FunctionCall.ID)
			}
		}
	}
	if len(calls) != 1 || calls[0] != "read:1" {
		t.Errorf("expected only read:1 to survive, got %v", calls)
	}
}

// Model message where ALL parts are orphan calls → drop the whole message.
func TestStripOrphanFunctionCalls_DropsEntirelyOrphanMessage(t *testing.T) {
	history := []*genai.Content{
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{Text: "original user message"},
			},
		},
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{FunctionCall: &genai.FunctionCall{ID: "x:1"}},
				{FunctionCall: &genai.FunctionCall{ID: "x:2"}},
			},
		},
		// No responses for x:1 or x:2 anywhere.
	}
	cleaned := stripOrphanFunctionCalls(history)
	if len(cleaned) != 1 {
		t.Fatalf("expected 1 message after dropping all-orphan model turn, got %d", len(cleaned))
	}
	if cleaned[0].Role != genai.RoleUser {
		t.Errorf("surviving message should be the user text; got role=%v", cleaned[0].Role)
	}
}

// Clean history with no orphans must survive intact (no allocation
// churn — the fn uses the original pointers for untouched messages).
func TestStripOrphanFunctionCalls_NoOrphansNoMutation(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "a:1"}},
		}},
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "a:1"}},
		}},
	}
	cleaned := stripOrphanFunctionCalls(history)
	if len(cleaned) != len(history) {
		t.Errorf("clean history changed length: %d → %d", len(history), len(cleaned))
	}
	// Original message pointers should be preserved for untouched messages
	// (performance signal — not a correctness requirement, but worth pinning).
	for i := range history {
		if cleaned[i] != history[i] {
			t.Errorf("message %d was reallocated despite being clean", i)
		}
	}
}

// Empty-ID FunctionCall parts must NOT be treated as orphan (they have
// no ID to pair, so we can't judge). fallbackToolID handles them later
// in the client.
func TestStripOrphanFunctionCalls_EmptyIDKept(t *testing.T) {
	history := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "", Name: "anon"}},
		}},
	}
	cleaned := stripOrphanFunctionCalls(history)
	if len(cleaned) != 1 || len(cleaned[0].Parts) != 1 {
		t.Errorf("empty-ID call should be preserved, got: %+v", cleaned)
	}
}

// trimToLastModelMessage end-to-end: orphan in the last model turn
// after an exhausted retry — old code kept it, new code strips it.
func TestTrimToLastModelMessage_StripsOrphanInKeptTurn(t *testing.T) {
	// Simulate: original history has 1 paired turn. Then a new request
	// added a model turn with 2 calls, but only 1 got a response before
	// the flow failed.
	history := []*genai.Content{
		// Original — preserved by minLen.
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "task"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "old:1"}},
		}},
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "old:1"}},
		}},
		// New model turn — the one trim-to-last-model keeps.
		{Role: genai.RoleModel, Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "new:paired"}},
			{FunctionCall: &genai.FunctionCall{ID: "new:orphan"}}, // no response
		}},
		{Role: genai.RoleUser, Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "new:paired"}},
		}},
	}
	minLen := 3 // preserve original 3 messages no matter what
	trimmed := trimToLastModelMessage(history, minLen)

	// The last model turn must still be present, with "new:orphan" dropped.
	var lastModelCalls []string
	for _, msg := range trimmed {
		if msg.Role == genai.RoleModel {
			for _, p := range msg.Parts {
				if p.FunctionCall != nil {
					lastModelCalls = append(lastModelCalls, p.FunctionCall.ID)
				}
			}
		}
	}
	foundOrphan := false
	for _, id := range lastModelCalls {
		if id == "new:orphan" {
			foundOrphan = true
		}
	}
	if foundOrphan {
		t.Errorf("trim+strip should have removed new:orphan; got calls %v", lastModelCalls)
	}
}
