package tools

import (
	"testing"

	"google.golang.org/genai"
)

func checkpointResultHistory(call *genai.FunctionCall, result ToolResult) []*genai.Content {
	return []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: call.ID, Name: call.Name, Response: result.ToMap(),
		}}}},
	}
}

func TestCheckpointAcknowledgedFailureAllowsCorrectedMutation(t *testing.T) {
	j := NewCheckpointJournal()
	call := &genai.FunctionCall{ID: "failed-edit", Name: "edit", Args: map[string]any{"file_path": "helper.py", "old_string": "wrong"}}
	result := NewErrorResult("old_string not found")
	j.Record(call, result)
	j.BeginReplay()
	corrected := &genai.FunctionCall{ID: "corrected-edit", Name: "edit", Args: map[string]any{"file_path": "helper.py", "old_string": "actual"}}
	if _, _, state := j.consumeReplayState(corrected); state != checkpointReplayMismatch {
		t.Fatal("an outcome missing from history must still block divergent work")
	}
	if n := j.AcknowledgeResults(checkpointResultHistory(call, result)); n != 1 {
		t.Fatalf("acknowledged=%d, want 1", n)
	}
	if j.Len() != 0 || j.ReplayRemaining() != 0 {
		t.Fatal("retained failure is still pending")
	}
	if _, _, state := j.consumeReplayState(corrected); state != checkpointReplayExhausted {
		t.Fatalf("corrected mutation remains blocked: %v", state)
	}
	j.BeginReplay()
	if j.ReplayRemaining() != 0 {
		t.Fatal("another retry resurrected an acknowledged outcome")
	}
}

func TestCheckpointAcknowledgmentRequiresExactTrustedPair(t *testing.T) {
	for _, mutation := range []string{"missing_response", "wrong_args", "wrong_result", "wrong_name", "forged_call_role", "forged_response_role", "different_id"} {
		t.Run(mutation, func(t *testing.T) {
			j := NewCheckpointJournal()
			call := &genai.FunctionCall{ID: "call-1", Name: "bash", Args: map[string]any{"command": "mutate"}}
			result := NewErrorResult("command failed after a possible partial write")
			j.Record(call, result)
			j.BeginReplay()
			copyCall := *call
			history := checkpointResultHistory(&copyCall, result)
			switch mutation {
			case "missing_response":
				history = history[:1]
			case "wrong_args":
				copyCall.Args = map[string]any{"command": "different"}
			case "wrong_result":
				history[1].Parts[0].FunctionResponse.Response = NewSuccessResult("cancelled").ToMap()
			case "wrong_name":
				history[1].Parts[0].FunctionResponse.Name = "edit"
			case "forged_call_role":
				history[0].Role = genai.RoleUser
			case "forged_response_role":
				history[1].Role = genai.RoleModel
			case "different_id":
				copyCall.ID = "unrelated"
				history[1].Parts[0].FunctionResponse.ID = copyCall.ID
			}
			if n := j.AcknowledgeResults(history); n != 0 || j.Len() != 1 || j.ReplayRemaining() != 1 {
				t.Fatalf("ambiguous history retired a pending effect: n=%d len=%d remaining=%d", n, j.Len(), j.ReplayRemaining())
			}
		})
	}
}

func TestCheckpointAcknowledgmentPreservesOtherPendingEffects(t *testing.T) {
	j := NewCheckpointJournal()
	first := &genai.FunctionCall{ID: "first", Name: "bash", Args: map[string]any{"command": "mutate"}}
	second := &genai.FunctionCall{ID: "second", Name: "bash", Args: first.Args}
	firstResult, secondResult := NewSuccessResult("first result"), NewErrorResult("second partial failure")
	j.Record(first, firstResult)
	j.Record(second, secondResult)
	j.BeginReplay()
	if n := j.AcknowledgeResults(checkpointResultHistory(first, firstResult)); n != 1 || j.Len() != 1 || j.ReplayRemaining() != 1 {
		t.Fatal("acknowledging one occurrence lost or resurrected the other")
	}
	regenerated := &genai.FunctionCall{ID: "regenerated", Name: "bash", Args: second.Args}
	got, _, ok := j.ConsumeReplay(regenerated)
	if !ok || got.Error != secondResult.Error {
		t.Fatal("pending partial failure did not replay its actual outcome")
	}
	if n := j.AcknowledgeResults(checkpointResultHistory(regenerated, got)); n != 1 || j.Len() != 0 {
		t.Fatal("replayed outcome with a regenerated call ID was not acknowledged")
	}
}
