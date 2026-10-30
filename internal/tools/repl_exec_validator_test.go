package tools

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/repl"

	"google.golang.org/genai"
)

func TestReplExecRejectionReachesModelWithoutAutomaticRetry(t *testing.T) {
	message := "REPL rejected call 'open' at line 1: direct file access is disabled. Use context.read_slice(path) instead. Do not retry unchanged code."
	fake := &fakeREPLExecutor{result: repl.Result{
		Generation: 1,
		Error:      &repl.ExecutionError{Type: "PermissionError", Message: message},
	}}
	registry := NewRegistry()
	registry.MustRegister(NewReplExecTool(fake))
	cl := &scriptedExecutorClient{
		model: "glm-5.3",
		responses: []*client.StreamingResponse{
			replValidatorCallStream("r0", `open("anything")`),
			buildExecutorTestTextStream("I will use context.read_slice instead."),
		},
	}
	executor := NewExecutor(registry, cl, time.Second)
	executor.preFlightChecks = false
	if _, _, err := executor.Execute(t.Context(), nil, "read a file"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || len(cl.functionResults) != 1 || len(cl.functionResults[0]) != 1 {
		t.Fatalf("calls=%d model result batches=%v, want exactly one rejection", fake.calls, cl.functionResults)
	}
	response := cl.functionResults[0][0].Response
	if success, _ := response["success"].(bool); success {
		t.Fatalf("rejection reached model as success: %v", response)
	}
	if failure, _ := response["error"].(string); !strings.Contains(failure, message) {
		t.Fatalf("model-visible error lost corrective guidance: %v", response)
	}
}

func TestReplExecIdenticalRejectionsTriggerStagnationGuard(t *testing.T) {
	fake := &fakeREPLExecutor{result: repl.Result{
		Generation: 1,
		Error:      &repl.ExecutionError{Type: "PermissionError", Message: "blocked private attribute '__class__'"},
	}}
	registry := NewRegistry()
	registry.MustRegister(NewReplExecTool(fake))
	cl := &scriptedExecutorClient{model: "glm-5.3"}
	for i := range 5 {
		cl.responses = append(cl.responses, replValidatorCallStream(fmt.Sprintf("r%d", i), `().__class__`))
	}
	executor := NewExecutor(registry, cl, time.Second)
	executor.preFlightChecks = false
	_, _, err := executor.Execute(t.Context(), nil, "inspect a value")
	if err == nil || !strings.Contains(err.Error(), "executor stagnation") {
		t.Fatalf("identical rejected cells escaped loop guard: err=%v", err)
	}
	if fake.calls != 4 || len(cl.functionResults) != 4 {
		t.Fatalf("calls=%d model result batches=%d, want four rejections before stopping the fifth", fake.calls, len(cl.functionResults))
	}
	if stagnationFingerprint("repl_exec", map[string]any{"code": `().__class__`}) ==
		stagnationFingerprint("repl_exec", map[string]any{"code": `{"k": 1}.get("k")`}) {
		t.Fatal("a corrected cell must have a different stagnation fingerprint")
	}
}

func replValidatorCallStream(id, code string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID: id, Name: "repl_exec", Args: map[string]any{"code": code},
		}},
		Done: true, FinishReason: genai.FinishReasonStop,
	})
}
