package testkit

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// --- SendFunctionResponse (0% → 100%) ---

func TestMockClient_SendFunctionResponse(t *testing.T) {
	m := NewMockClient().EnqueueText("ok")
	resp, err := m.SendFunctionResponse(context.Background(), nil, []*genai.FunctionResponse{
		{Name: "read_file", Response: map[string]any{"output": "data"}},
	})
	if err != nil {
		t.Fatalf("SendFunctionResponse: %v", err)
	}
	if _, err := resp.Collect(); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	calls := m.Calls()
	if len(calls) != 1 || calls[0].Method != "SendFunctionResponse" {
		t.Fatalf("expected 1 SendFunctionResponse call, got %+v", calls)
	}
}

// --- GetTools / SetTools (0% → 100%) ---

func TestMockClient_GetSetTools(t *testing.T) {
	m := NewMockClient()
	if tools := m.GetTools(); len(tools) != 0 {
		t.Fatalf("expected 0 tools initially, got %d", len(tools))
	}

	m.SetTools([]*genai.Tool{{}})
	if tools := m.GetTools(); len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
}

// --- SetRateLimiter (0% → 100%) ---

func TestMockClient_SetRateLimiter(t *testing.T) {
	m := NewMockClient()
	limiter := "test-limiter"
	m.SetRateLimiter(limiter)
	if m.rateLimiter != limiter {
		t.Fatalf("expected rateLimiter to be set")
	}
}

// --- GetRawClient (0% → 100%) ---

func TestMockClient_GetRawClient(t *testing.T) {
	m := NewMockClient()
	if raw := m.GetRawClient(); raw != nil {
		t.Fatalf("expected nil rawClient, got %v", raw)
	}

	m.rawClient = "test-raw"
	if raw := m.GetRawClient(); raw != "test-raw" {
		t.Fatalf("expected 'test-raw', got %v", raw)
	}
}

// --- SetSystemInstruction / SystemInstruction (0% → 100%) ---

func TestMockClient_SystemInstruction(t *testing.T) {
	m := NewMockClient()
	if got := m.SystemInstruction(); got != "" {
		t.Fatalf("expected empty instruction, got %q", got)
	}

	m.SetSystemInstruction("you are a coding agent")
	if got := m.SystemInstruction(); got != "you are a coding agent" {
		t.Fatalf("expected 'you are a coding agent', got %q", got)
	}
}

// --- SetTurnContext / TurnContext (0% → 100%) ---

func TestMockClient_TurnContext(t *testing.T) {
	m := NewMockClient()
	if got := m.TurnContext(); got != "" {
		t.Fatalf("expected empty turn context, got %q", got)
	}

	m.SetTurnContext("session-123")
	if got := m.TurnContext(); got != "session-123" {
		t.Fatalf("expected 'session-123', got %q", got)
	}
}

// --- SetThinkingBudget / ThinkingBudget (0% → 100%) ---

func TestMockClient_ThinkingBudget(t *testing.T) {
	m := NewMockClient()
	if got := m.ThinkingBudget(); got != 0 {
		t.Fatalf("expected 0 budget, got %d", got)
	}

	m.SetThinkingBudget(8192)
	if got := m.ThinkingBudget(); got != 8192 {
		t.Fatalf("expected 8192, got %d", got)
	}
}

// --- Close (0% → 100%) ---

func TestMockClient_Close(t *testing.T) {
	m := NewMockClient()
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- cloneContents nil path (80% → 100%) ---

func TestCloneContents_Nil(t *testing.T) {
	if got := cloneContents(nil); got != nil {
		t.Fatalf("cloneContents(nil) = %v, want nil", got)
	}
}

// --- cloneResponses (0% → 100%) ---

func TestCloneResponses_Nil(t *testing.T) {
	if got := cloneResponses(nil); got != nil {
		t.Fatalf("cloneResponses(nil) = %v, want nil", got)
	}
}

func TestCloneResponses_Copy(t *testing.T) {
	in := []*genai.FunctionResponse{{Name: "tool1"}, {Name: "tool2"}}
	out := cloneResponses(in)
	if len(out) != 2 || out[0].Name != "tool1" || out[1].Name != "tool2" {
		t.Fatalf("cloneResponses = %+v, want copy of %+v", out, in)
	}
	// cloneResponses does a shallow slice copy — replacing an element in the
	// original slice should not affect the clone.
	in[0] = &genai.FunctionResponse{Name: "replaced"}
	if out[0].Name != "tool1" {
		t.Fatal("cloneResponses should produce independent slice")
	}
}

// --- ResolvedTempDir (0% → 100%) ---

func TestResolvedTempDir(t *testing.T) {
	dir := ResolvedTempDir(t)
	if dir == "" {
		t.Fatal("ResolvedTempDir should return non-empty path")
	}
}
