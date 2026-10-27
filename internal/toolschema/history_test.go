package toolschema

import (
	"encoding/json"
	"testing"

	"google.golang.org/genai"
)

func TestEffectiveHistoryPreservesWireAndRequiresMatchingPairs(t *testing.T) {
	for _, id := range []string{"call-id", ""} {
		t.Run("id="+id, func(t *testing.T) {
			call := &genai.FunctionCall{ID: id, Name: "use_tool", Args: map[string]any{"name": "skill", "arguments": map[string]any{"name": "check"}}}
			response := &genai.FunctionResponse{ID: id, Name: "use_tool", Response: map[string]any{"success": true, "content": "instructions"}}
			mismatched := &genai.FunctionResponse{ID: "other-id", Name: "use_tool"}
			history := []*genai.Content{
				nil,
				{Role: genai.RoleModel, Parts: []*genai.Part{nil, {FunctionCall: call}}},
				{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: mismatched}, {FunctionResponse: response}}},
			}
			before, _ := json.Marshal(history)
			view := EffectiveHistory(history)
			if view[1].Parts[1].FunctionCall.Name != "skill" || view[2].Parts[1].FunctionResponse.Name != "skill" || view[2].Parts[0].FunctionResponse.Name != "use_tool" {
				t.Fatal("incorrect effective identities", view)
			}
			if view[1].Parts[1].FunctionCall.Args["name"] != "check" {
				t.Fatal("arguments not unwrapped")
			}
			after, _ := json.Marshal(history)
			if string(before) != string(after) {
				t.Fatal("wire history changed")
			}
		})
	}
}

func TestEffectiveCallRejectsMalformedDispatch(t *testing.T) {
	for _, args := range []map[string]any{
		{"name": "skill"},
		{"name": "skill", "arguments": nil},
		{"name": "skill ", "arguments": map[string]any{}},
		{"name": "tool_search", "arguments": map[string]any{}},
		{"name": "use_tool", "arguments": map[string]any{}},
		{"name": "skill", "arguments": map[string]any{}, "extra": true},
	} {
		call := &genai.FunctionCall{Name: "use_tool", Args: args}
		if EffectiveCall(call) != call {
			t.Fatalf("resolved malformed dispatch: %v", args)
		}
	}
}
