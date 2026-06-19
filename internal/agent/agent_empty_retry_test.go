package agent

import (
	"testing"

	"gokin/internal/client"

	"google.golang.org/genai"
)

func userFuncResult() *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "read"}}}}
}
func modelText(s string) *genai.Content {
	return &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{genai.NewPartFromText(s)}}
}

// TestShouldRetryEmptyAfterTools pins the guard that decides an empty response is
// a transient empty-after-tools 200 worth re-sending — only fired after a real
// tool-results round, never for text/calls/max_tokens/initial responses.
func TestShouldRetryEmptyAfterTools(t *testing.T) {
	afterTools := []*genai.Content{userFuncResult(), modelText(" ")}
	afterText := []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{genai.NewPartFromText("do it")}}, modelText(" ")}

	empty := &client.Response{}
	withText := &client.Response{Text: "done"}
	withCalls := &client.Response{FunctionCalls: []*genai.FunctionCall{{Name: "edit"}}}
	maxTok := &client.Response{FinishReason: genai.FinishReasonMaxTokens}

	cases := []struct {
		name string
		hist []*genai.Content
		resp *client.Response
		want bool
	}{
		{"empty after tools", afterTools, empty, true},
		{"empty after text (not a tool round)", afterText, empty, false},
		{"has text", afterTools, withText, false},
		{"has calls", afterTools, withCalls, false},
		{"max_tokens truncation", afterTools, maxTok, false},
		{"nil resp", afterTools, nil, false},
		{"history too short", []*genai.Content{modelText(" ")}, empty, false},
	}
	for _, tc := range cases {
		if got := shouldRetryEmptyAfterTools(tc.hist, tc.resp); got != tc.want {
			t.Errorf("%s: shouldRetryEmptyAfterTools = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPopEmptyModelPlaceholder pins the one genuinely-novel piece: the
// side-effect-free pop only removes the exact empty " " placeholder, never a real
// model turn (so the re-send can't corrupt history).
func TestPopEmptyModelPlaceholder(t *testing.T) {
	// Last entry is the exact placeholder → popped; history ends in the tool round.
	a := &Agent{history: []*genai.Content{userFuncResult(), modelText(" ")}}
	if !a.popEmptyModelPlaceholder() {
		t.Fatal("should pop the empty placeholder")
	}
	if n := len(a.history); n != 1 || !contentHasFunctionResponse(a.history[n-1]) {
		t.Fatalf("after pop, history should end in the tool-results turn, got len=%d", len(a.history))
	}

	// Last entry is a REAL model turn → not popped, left intact.
	a2 := &Agent{history: []*genai.Content{userFuncResult(), modelText("real answer")}}
	if a2.popEmptyModelPlaceholder() {
		t.Error("must NOT pop a real model turn")
	}
	if len(a2.history) != 2 {
		t.Error("a real model turn must be left intact")
	}

	// A model turn with multiple parts (not the lone placeholder) → not popped.
	multi := &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{genai.NewPartFromText(" "), genai.NewPartFromText("x")}}
	a3 := &Agent{history: []*genai.Content{userFuncResult(), multi}}
	if a3.popEmptyModelPlaceholder() {
		t.Error("must NOT pop a multi-part model turn")
	}

	// Empty history → false, no panic.
	if (&Agent{}).popEmptyModelPlaceholder() {
		t.Error("empty history should return false")
	}
}
