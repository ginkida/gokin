package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// Focus: pure-function behavior of the budget gate and the synthetic
// response builder. The end-to-end loop integration is covered by the
// existing scripted-client executor tests — adding a fresh scripted
// conversation here would duplicate that scaffolding without added
// signal.

func TestShouldEnforceToolBudget_DisabledWhenZero(t *testing.T) {
	e := &Executor{toolBudget: 0}
	if e.shouldEnforceToolBudget("deepseek-v4-pro", 99) {
		t.Errorf("expected disabled budget to never fire")
	}
}

func TestShouldEnforceToolBudget_BudgetedFamiliesOnly(t *testing.T) {
	e := &Executor{toolBudget: 10}
	// Every hosted family is quota-capped.
	for _, model := range []string{"claude-sonnet-4", "glm-5.2", "deepseek-v4-pro", "deepseek-v4-pro", "deepseek-v4-pro"} {
		if !e.shouldEnforceToolBudget(model, 100) {
			t.Errorf("budget not applied to hosted model %s", model)
		}
	}
	// Local Ollama inference does not consume a hosted quota.
	if e.shouldEnforceToolBudget("llama3", 100) {
		t.Errorf("budget must not apply to local Ollama family")
	}
	if e.shouldEnforceToolBudget("deepseek-v4-pro", 5) {
		t.Errorf("GLM under budget must not fire")
	}
}

func TestShouldEnforceToolBudget_TriggersAtThreshold(t *testing.T) {
	e := &Executor{toolBudget: 5}
	if e.shouldEnforceToolBudget("deepseek-v4-pro", 4) {
		t.Errorf("fired too early (consumed=4 < budget=5)")
	}
	if !e.shouldEnforceToolBudget("deepseek-v4-pro", 5) {
		t.Errorf("should fire at exact threshold (consumed=5 == budget=5)")
	}
	if !e.shouldEnforceToolBudget("deepseek-v4-pro", 99) {
		t.Errorf("should fire when past threshold")
	}
}

func TestSetToolBudget_ClampsAndDisables(t *testing.T) {
	e := &Executor{}
	e.SetToolBudget(-5)
	if e.toolBudget != 0 {
		t.Errorf("negative should disable, got %d", e.toolBudget)
	}
	e.SetToolBudget(0)
	if e.toolBudget != 0 {
		t.Errorf("zero should disable, got %d", e.toolBudget)
	}
	e.SetToolBudget(5) // below min
	if e.toolBudget != 10 {
		t.Errorf("below min should clamp to 10, got %d", e.toolBudget)
	}
	e.SetToolBudget(40)
	if e.toolBudget != 40 {
		t.Errorf("explicit 40 should survive, got %d", e.toolBudget)
	}
}

func TestBuildToolBudgetExhaustedResults_PreservesCallIDs(t *testing.T) {
	e := &Executor{toolBudget: 10}
	calls := []*genai.FunctionCall{
		{ID: "call-1", Name: "read"},
		{ID: "call-2", Name: "grep"},
	}
	results := e.buildToolBudgetExhaustedResults(calls)
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].ID != "call-1" || results[0].Name != "read" {
		t.Errorf("call 1 mismatch: %+v", results[0])
	}
	if results[1].ID != "call-2" || results[1].Name != "grep" {
		t.Errorf("call 2 mismatch: %+v", results[1])
	}
	// Both responses must be marked as errors so the model treats them
	// as signals rather than valid tool output.
	for i, r := range results {
		success, _ := r.Response["success"].(bool)
		if success {
			t.Errorf("result %d marked success — should be error", i)
		}
		errMsg, _ := r.Response["error"].(string)
		if !strings.Contains(errMsg, "Tool budget guard") {
			t.Errorf("result %d missing budget guard message: %v", i, r.Response)
		}
	}
}

func TestBuildToolBudgetExhaustedResults_HandlesNilCall(t *testing.T) {
	e := &Executor{toolBudget: 10}
	// One nil call (defensive — shouldn't happen in practice but the
	// synthesizer must not panic on it).
	calls := []*genai.FunctionCall{nil, {ID: "x", Name: "bash"}}
	results := e.buildToolBudgetExhaustedResults(calls)
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].Name != "tool" {
		t.Errorf("nil call should fall back to generic 'tool', got %q", results[0].Name)
	}
}

func TestBuildToolBudgetMessage_MentionsBudget(t *testing.T) {
	msg := buildToolBudgetMessage(25)
	if !strings.Contains(msg, "25") {
		t.Errorf("missing budget value: %q", msg)
	}
	if !strings.Contains(msg, "final answer") {
		t.Errorf("missing finalize directive: %q", msg)
	}
}

// Integration: with a 3-budget ceiling, the 4th iteration's tool calls
// get short-circuited into synthetic budget-exhausted responses, and
// DeepSeek's final text response lands after that synthetic round. Uses
// varied file paths so stagnation guard stays out of the way.
func TestExecutorExecuteLoop_ToolBudgetForcesFinalization(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "deepseek-v4-pro",
		responses: []*client.StreamingResponse{
			buildBudgetReadStream("b0", "a.go"),
			buildBudgetReadStream("b1", "b.go"),
			buildBudgetReadStream("b2", "c.go"),
			buildBudgetReadStream("b3", "d.go"),
			buildExecutorTestTextStream("Finalized within budget."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	// Direct field write — the public setter clamps anything <10 up to 10
	// (prevents user-config mistakes). Tests need a smaller value to keep
	// scripted responses short; the setter itself is covered in the unit
	// test above.
	exec.toolBudget = 3

	_, finalText, err := exec.Execute(context.Background(), nil, "survey Go files")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "Finalized within budget." {
		t.Fatalf("finalText = %q, want %q", finalText, "Finalized within budget.")
	}
	// The first 3 tool calls execute normally; the 4th gets the synthetic
	// budget response without calling the tool.
	if readTool.calls != 3 {
		t.Errorf("read tool calls = %d, want 3 (first 3 before budget kicks in)", readTool.calls)
	}
	if len(cl.functionResults) != 4 {
		t.Fatalf("SendFunctionResponse calls = %d, want 4 (3 real + 1 synthetic)", len(cl.functionResults))
	}

	// The last batch must carry the budget-guard error.
	last := cl.functionResults[len(cl.functionResults)-1]
	if len(last) != 1 {
		t.Fatalf("last batch len = %d, want 1", len(last))
	}
	success, _ := last[0].Response["success"].(bool)
	if success {
		t.Errorf("budget-guard response should be marked unsuccessful: %+v", last[0].Response)
	}
	errMsg, _ := last[0].Response["error"].(string)
	if !strings.Contains(errMsg, "Tool budget guard") {
		t.Errorf("missing budget-guard message: %q", errMsg)
	}
}

func TestExecutorExecuteLoop_ToolBudgetZeroKeepsAllCalls(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "deepseek-v4-pro",
		responses: []*client.StreamingResponse{
			buildBudgetReadStream("z0", "a.go"),
			buildBudgetReadStream("z1", "b.go"),
			buildBudgetReadStream("z2", "c.go"),
			buildBudgetReadStream("z3", "d.go"),
			buildExecutorTestTextStream("All four read."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	exec.SetToolBudget(0) // disabled

	_, finalText, err := exec.Execute(context.Background(), nil, "survey Go files")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "All four read." {
		t.Fatalf("finalText = %q, want %q", finalText, "All four read.")
	}
	if readTool.calls != 4 {
		t.Errorf("expected all 4 reads when budget=0, got %d", readTool.calls)
	}
}

func TestExecutorExecuteLoop_GLMUsesHostedBudget(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "glm-5.1",
		responses: []*client.StreamingResponse{
			buildBudgetReadStream("g0", "a.go"),
			buildBudgetReadStream("g1", "b.go"),
			buildBudgetReadStream("g2", "c.go"),
			buildBudgetReadStream("g3", "d.go"),
			buildExecutorTestTextStream("GLM ran all four."),
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	exec.toolBudget = 3

	_, finalText, err := exec.Execute(context.Background(), nil, "survey Go files")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if finalText != "GLM ran all four." {
		t.Fatalf("finalText = %q, want %q", finalText, "GLM ran all four.")
	}
	if readTool.calls != 3 {
		t.Errorf("GLM should stop executing at hosted budget; got %d reads", readTool.calls)
	}
}

func TestExecutorExecuteLoop_BudgetStopsLocallyWhenFinalizeIgnored(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildBudgetReadStream("g0", "a.go"),
			buildBudgetReadStream("g1", "b.go"),
			buildBudgetReadStream("g2", "c.go"),
			buildBudgetReadStream("g3", "d.go"), // first synthetic guard response
			buildBudgetReadStream("g4", "e.go"), // ignores finalize; stop locally
		},
	}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	exec.toolBudget = 3

	_, finalText, err := exec.Execute(context.Background(), nil, "survey Go files")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(finalText, "Stopped after") || !strings.Contains(finalText, "ask me to continue") {
		t.Fatalf("finalText = %q, want honest local budget stop", finalText)
	}
	if readTool.calls != 3 {
		t.Fatalf("read tool calls = %d, want only first 3", readTool.calls)
	}
	if len(cl.functionResults) != 4 {
		t.Fatalf("provider function rounds = %d, want 4 (3 real + 1 finalize)", len(cl.functionResults))
	}
}

// buildBudgetReadStream mints a stream that reads a different file
// each turn — avoids tripping the identical-pattern stagnation guard so
// only the budget check decides when to stop.
func buildBudgetReadStream(id, filePath string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "read",
			Args: map[string]any{
				"file_path": filePath,
				"offset":    1.0,
				"limit":     100.0,
			},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}
