package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// TestExecutor_PlanModeBlocksNonReadOnlyTools proves the defense-in-depth
// gate: the executor refuses to dispatch write/execute tools when the
// plan-mode check returns true, even if the model hallucinates the call.
//
// Why this matters: the schema filter stops the model from SEEING write
// tools in its tool list, but a well-trained model can still emit a
// `FunctionCall{Name: "write"}` from pre-training priors — especially
// when the user's prompt literally names a file-write operation. Without
// the executor-level gate, that hallucinated call would land in
// registry.Get, find `write`, and happily execute it.
func TestExecutor_PlanModeBlocksNonReadOnlyTools(t *testing.T) {
	registry := NewRegistry()
	// Register both a read-only and a write tool so we can assert the
	// gate distinguishes them correctly.
	registry.MustRegister(&scriptedTool{name: "read", ran: new(bool)})
	writeRan := new(bool)
	registry.MustRegister(&scriptedTool{name: "write", ran: writeRan})

	exec := NewExecutor(registry, nil, time.Second)

	// Flag the executor as being in plan mode. The predicate is invoked
	// on every dispatch; returning true here simulates the app-level
	// plan-mode state.
	planMode := true
	exec.SetPlanModeCheck(func() bool { return planMode })

	// Call `write` — must be blocked with a clear error message that
	// names the tool and points the model at enter_plan_mode.
	got := exec.doExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "write",
		Args: map[string]any{"file_path": "x.go", "content": "package x"},
	})
	if got.Success {
		t.Fatal("executor must block `write` in plan mode; got Success=true")
	}
	// Error text is on the Error field (NewErrorResult puts it there).
	msg := got.Error
	if msg == "" {
		msg = got.Content
	}
	if !strings.Contains(msg, "plan mode") {
		t.Errorf("error message should name plan mode so the model understands why, got: %q", msg)
	}
	if !strings.Contains(msg, "enter_plan_mode") {
		t.Errorf("error message should direct the model to enter_plan_mode, got: %q", msg)
	}
	if *writeRan {
		t.Error("write tool should NOT have been invoked — schema-filter-bypass is the bug this test guards")
	}
}

// TestExecutor_PlanModeAllowsReadOnlyTools verifies the gate's allow-list
// side: read-only tools (read, grep, enter_plan_mode, ...) still dispatch.
func TestExecutor_PlanModeAllowsReadOnlyTools(t *testing.T) {
	registry := NewRegistry()
	readRan := new(bool)
	registry.MustRegister(&scriptedTool{name: "read", ran: readRan})
	grepRan := new(bool)
	registry.MustRegister(&scriptedTool{name: "grep", ran: grepRan})
	enterRan := new(bool)
	registry.MustRegister(&scriptedTool{name: "enter_plan_mode", ran: enterRan})

	exec := NewExecutor(registry, nil, time.Second)
	exec.SetPlanModeCheck(func() bool { return true })

	for _, name := range []string{"read", "grep", "enter_plan_mode"} {
		got := exec.doExecuteTool(context.Background(), &genai.FunctionCall{
			Name: name,
			Args: map[string]any{},
		})
		if !got.Success {
			t.Errorf("plan-mode-safe tool %q was blocked: %q", name, got.Content)
		}
	}
	if !*readRan || !*grepRan || !*enterRan {
		t.Errorf("all allow-list tools must execute; read=%v grep=%v enter=%v",
			*readRan, *grepRan, *enterRan)
	}
}

// TestExecutor_PlanModeCheckNil_NoGate is the "disable plan-mode gating"
// path. Sub-agents, tests, and the main agent in normal mode all leave the
// callback unset; the executor must treat nil as "always full access".
//
// Uses `read` (a read-only tool) to isolate the nil-callback path — write
// tools may trip other executor guards (permission manager, pre-flight
// safety, delta-check barrier) that are unrelated to plan mode. The
// relevant invariant is simply: nil callback = gate is inactive =
// executor behaves as it did before the feature.
func TestExecutor_PlanModeCheckNil_NoGate(t *testing.T) {
	registry := NewRegistry()
	readRan := new(bool)
	registry.MustRegister(&scriptedTool{name: "read", ran: readRan})

	exec := NewExecutor(registry, nil, time.Second)
	// No SetPlanModeCheck — planModeCheck stays nil.

	got := exec.doExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "read",
		Args: map[string]any{},
	})
	// The critical invariant: the gate must not reject the call just
	// because the predicate is nil. Failure would be got.Error
	// containing "plan mode".
	if got.Error != "" && strings.Contains(got.Error, "plan mode") {
		t.Errorf("nil planModeCheck must not block; got plan-mode error: %q", got.Error)
	}
	if !*readRan {
		t.Error("read tool should have been invoked with no gate")
	}
}

// scriptedTool records whether it was executed and always succeeds. Used
// in the plan-mode tests to distinguish "blocked (didn't run)" from
// "allowed (did run)".
type scriptedTool struct {
	name string
	ran  *bool
}

func (t *scriptedTool) Name() string        { return t.name }
func (t *scriptedTool) Description() string { return "scripted-" + t.name }
func (t *scriptedTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.name,
		Description: t.Description(),
	}
}
func (t *scriptedTool) Validate(_ map[string]any) error { return nil }
func (t *scriptedTool) Execute(_ context.Context, _ map[string]any) (ToolResult, error) {
	if t.ran != nil {
		*t.ran = true
	}
	return NewSuccessResult("ran " + t.name), nil
}

// compile-time check: scriptedTool satisfies Tool.
var _ Tool = (*scriptedTool)(nil)

// Silence unused-import warnings when client isn't referenced in these
// specific tests — executor's NewExecutor takes a client.Client parameter.
var _ client.Client = (*nilClient)(nil)

type nilClient struct{}

func (n *nilClient) SendMessage(_ context.Context, _ string) (*client.StreamingResponse, error) {
	return nil, nil
}
func (n *nilClient) SendMessageWithHistory(_ context.Context, _ []*genai.Content, _ string) (*client.StreamingResponse, error) {
	return nil, nil
}
func (n *nilClient) SendFunctionResponse(_ context.Context, _ []*genai.Content, _ []*genai.FunctionResponse) (*client.StreamingResponse, error) {
	return nil, nil
}
func (n *nilClient) SetTools(_ []*genai.Tool)                   {}
func (n *nilClient) SetRateLimiter(_ any)                       {}
func (n *nilClient) GetModel() string                           { return "" }
func (n *nilClient) SetModel(_ string)                          {}
func (n *nilClient) WithModel(_ string) client.Client           { return n }
func (n *nilClient) GetRawClient() any                          { return nil }
func (n *nilClient) SetSystemInstruction(_ string)              {}
func (n *nilClient) SetThinkingBudget(_ int32)                  {}
func (n *nilClient) Close() error                               { return nil }
func (n *nilClient) CountTokens(_ context.Context, _ []*genai.Content) (*genai.CountTokensResponse, error) {
	return &genai.CountTokensResponse{}, nil
}
