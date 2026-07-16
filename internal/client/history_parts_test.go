package client

import (
	"testing"

	"google.golang.org/genai"
)

func TestResponsePartsForHistory_MergesSignedThinkingTextAndToolCall(t *testing.T) {
	call := &genai.FunctionCall{ID: "tool-1", Name: "read", Args: map[string]any{"path": "main.go"}}
	thought := &genai.Part{Thought: true, Text: "inspect first", ThoughtSignature: []byte("signed")}
	callPart := &genai.Part{FunctionCall: call}
	resp := &Response{
		Text:          "I'll inspect it.",
		FunctionCalls: []*genai.FunctionCall{call},
		Parts:         []*genai.Part{thought, callPart},
	}

	parts := ResponsePartsForHistory(resp)
	if len(parts) != 3 {
		t.Fatalf("parts len = %d, want 3", len(parts))
	}
	if parts[0] != thought || !parts[0].Thought || string(parts[0].ThoughtSignature) != "signed" {
		t.Fatalf("signed thought was not preserved: %#v", parts[0])
	}
	if parts[1].Text != "I'll inspect it." || parts[1].Thought {
		t.Fatalf("ordinary text = %#v, want merged text part", parts[1])
	}
	if parts[2] != callPart {
		t.Fatalf("tool call part was replaced or duplicated: %#v", parts)
	}
	if len(resp.Parts) != 2 {
		t.Fatalf("response Parts mutated: len = %d, want 2", len(resp.Parts))
	}
}

func TestResponsePartsForHistory_DoesNotDuplicateExistingTextOrCallID(t *testing.T) {
	existing := &genai.FunctionCall{ID: "tool-1", Name: "read"}
	duplicateInstance := &genai.FunctionCall{ID: "tool-1", Name: "read"}
	textPart := genai.NewPartFromText("done")
	resp := &Response{
		Text:          "done",
		FunctionCalls: []*genai.FunctionCall{duplicateInstance},
		Parts: []*genai.Part{
			textPart,
			{FunctionCall: existing},
		},
	}

	parts := ResponsePartsForHistory(resp)
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2: %#v", len(parts), parts)
	}
	if parts[0] != textPart || parts[1].FunctionCall != existing {
		t.Fatalf("existing canonical parts changed: %#v", parts)
	}
}

func TestResponsePartsForHistory_Nil(t *testing.T) {
	if parts := ResponsePartsForHistory(nil); parts != nil {
		t.Fatalf("nil response returned %#v, want nil", parts)
	}
}

func TestResponsePartsForHistory_AddsTextMissingFromPartialParts(t *testing.T) {
	resp := &Response{
		Text:  "hello world",
		Parts: []*genai.Part{genai.NewPartFromText("hello ")},
	}
	parts := ResponsePartsForHistory(resp)
	if len(parts) != 2 || parts[0].Text+parts[1].Text != resp.Text {
		t.Fatalf("parts = %#v, want complete text %q", parts, resp.Text)
	}
}

func TestResponsePartsForHistory_AppendsPartialSuffixAfterLastInterleavedText(t *testing.T) {
	call := &genai.FunctionCall{ID: "tool-1", Name: "read"}
	resp := &Response{
		Text: "abc",
		Parts: []*genai.Part{
			genai.NewPartFromText("a"),
			{FunctionCall: call},
			genai.NewPartFromText("b"),
		},
	}
	parts := ResponsePartsForHistory(resp)
	if len(parts) != 4 || parts[0].Text != "a" || parts[1].FunctionCall != call ||
		parts[2].Text != "b" || parts[3].Text != "c" {
		t.Fatalf("parts reordered while adding suffix: %#v", parts)
	}
}

func TestResponsePartsForHistory_ReconcilesDivergentTextWithAggregate(t *testing.T) {
	resp := &Response{
		Text:  "canonical complete answer",
		Parts: []*genai.Part{genai.NewPartFromText("stale partial")},
	}
	parts := ResponsePartsForHistory(resp)
	if len(parts) != 1 || parts[0].Text != resp.Text {
		t.Fatalf("parts = %#v, want canonical aggregate text", parts)
	}
}
