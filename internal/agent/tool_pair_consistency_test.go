package agent

import (
	"testing"

	"google.golang.org/genai"
)

func tpcCall(id, name string) *genai.Part {
	return &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: name}}
}

func tpcResp(id, name string) *genai.Part {
	return &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: map[string]any{"ok": true}}}
}

func tpcContent(role genai.Role, parts ...*genai.Part) *genai.Content {
	return &genai.Content{Role: string(role), Parts: parts}
}

type tpcIDs struct{ calls, responses map[string]bool }

func tpcCollect(history []*genai.Content) tpcIDs {
	ids := tpcIDs{calls: map[string]bool{}, responses: map[string]bool{}}
	for _, m := range history {
		if m == nil {
			continue
		}
		for _, p := range m.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil && p.FunctionCall.ID != "" {
				ids.calls[p.FunctionCall.ID] = true
			}
			if p.FunctionResponse != nil && p.FunctionResponse.ID != "" {
				ids.responses[p.FunctionResponse.ID] = true
			}
		}
	}
	return ids
}

// ensureToolPairConsistency is the safety net that prevents post-compaction
// 400s: providers require every FunctionCall to be followed by a matching
// FunctionResponse (and vice versa). After truncation drops messages, orphaned
// halves must be stripped. The invariant: NO orphan may survive.
func TestEnsureToolPairConsistency_RemovesOrphans(t *testing.T) {
	history := []*genai.Content{
		tpcContent(genai.RoleUser, genai.NewPartFromText("hello")),
		tpcContent(genai.RoleModel, tpcCall("keep", "read")),       // paired call
		tpcContent(genai.RoleUser, tpcResp("keep", "read")),        // paired response
		tpcContent(genai.RoleModel, tpcCall("orphanCall", "grep")), // orphan: call w/o response
		tpcContent(genai.RoleUser, tpcResp("orphanResp", "bash")),  // orphan: response w/o call
	}
	out := ensureToolPairConsistency(history)
	ids := tpcCollect(out)

	if !ids.calls["keep"] || !ids.responses["keep"] {
		t.Error("paired call/response wrongly removed")
	}
	if ids.calls["orphanCall"] {
		t.Error("orphan call not removed")
	}
	if ids.responses["orphanResp"] {
		t.Error("orphan response not removed")
	}
	// THE invariant: every surviving call has a response and vice versa.
	for id := range ids.calls {
		if !ids.responses[id] {
			t.Errorf("call %q has no matching response after cleanup", id)
		}
	}
	for id := range ids.responses {
		if !ids.calls[id] {
			t.Errorf("response %q has no matching call after cleanup", id)
		}
	}
}

// A message carrying both legitimate text AND an orphaned call must survive —
// only the orphan part is stripped, the text is preserved.
func TestEnsureToolPairConsistency_KeepsTextDropsOrphanPart(t *testing.T) {
	history := []*genai.Content{
		tpcContent(genai.RoleModel, genai.NewPartFromText("reasoning"), tpcCall("orphan", "read")),
	}
	out := ensureToolPairConsistency(history)
	if len(out) != 1 {
		t.Fatalf("expected the text-bearing message to survive, got %d messages", len(out))
	}
	var hasText, hasCall bool
	for _, p := range out[0].Parts {
		if p.Text != "" {
			hasText = true
		}
		if p.FunctionCall != nil {
			hasCall = true
		}
	}
	if !hasText {
		t.Error("legitimate text part dropped")
	}
	if hasCall {
		t.Error("orphan call part kept")
	}
}

// No orphans → the function is a no-op and returns the input unchanged.
func TestEnsureToolPairConsistency_NoOrphansUnchanged(t *testing.T) {
	history := []*genai.Content{
		tpcContent(genai.RoleModel, tpcCall("a", "read")),
		tpcContent(genai.RoleUser, tpcResp("a", "read")),
	}
	out := ensureToolPairConsistency(history)
	if len(out) != len(history) {
		t.Fatalf("balanced history length changed: %d → %d", len(history), len(out))
	}
	ids := tpcCollect(out)
	if !ids.calls["a"] || !ids.responses["a"] {
		t.Error("balanced pair removed by a supposed no-op")
	}
}

// nil messages and nil parts must be skipped, not panicked on.
func TestEnsureToolPairConsistency_NilSafe(t *testing.T) {
	history := []*genai.Content{
		nil,
		tpcContent(genai.RoleModel, tpcCall("a", "read")),
		tpcContent(genai.RoleUser, nil, tpcResp("a", "read")), // nil part mixed with a valid response
		tpcContent(genai.RoleModel, tpcCall("orphan", "grep")),
		nil,
	}
	out := ensureToolPairConsistency(history) // must not panic
	ids := tpcCollect(out)
	if !ids.calls["a"] || !ids.responses["a"] {
		t.Error("valid pair lost amid nils")
	}
	if ids.calls["orphan"] {
		t.Error("orphan call survived")
	}
	for _, m := range out {
		if m == nil {
			t.Error("nil message survived into output")
		}
	}
}
