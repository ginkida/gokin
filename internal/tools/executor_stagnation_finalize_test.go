package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// buildExecutorTestBashStream scripts one model round that calls bash with the
// given command. IDs must be unique per round — the side-effect ledger dedups
// by call.ID and would replay repeats instead of executing them.
func buildExecutorTestBashStream(id, command string) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{
			ID:   id,
			Name: "bash",
			Args: map[string]any{"command": command},
		}},
		Done:         true,
		FinishReason: genai.FinishReasonStop,
	})
}

// The field-report loop, second round (v0.100.90): a model that narrates and
// re-runs the same read-only inspection command IGNORING both recovery hints
// used to hard-abort the whole turn. A pure inspection loop must end in an
// honest final answer, not a dead turn — after the hint budget the executor
// now cuts the call off and demands the answer (force-finalize phase), and
// only persistence past THAT aborts.
func TestExecutorExecuteLoop_ReadOnlyBashLoopFinalizesInsteadOfAbort(t *testing.T) {
	registry := NewRegistry()
	bashTool := &scriptedStaticTool{name: "bash", content: "On branch main\nnothing to commit"}
	if err := registry.Register(bashTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	const cmd = `git status --short && echo "---LOG---" && git log --oneline `
	// 4 executed + hint(5th) + hint(6th) + finalize(7th) + finalize(8th), then
	// the model finally answers.
	responses := make([]*client.StreamingResponse, 0, 9)
	for i := 0; i < 8; i++ {
		responses = append(responses, buildExecutorTestBashStream(
			"b"+string(rune('0'+i)), cmd))
	}
	responses = append(responses, buildExecutorTestTextStream("Final answer from what I already saw."))
	cl := &scriptedExecutorClient{model: "glm-5.2", responses: responses}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	_, finalText, err := exec.Execute(context.Background(), nil, "check repo state")
	if err != nil {
		t.Fatalf("Execute() error = %v, want graceful finalize (no abort) for read-only bash loop", err)
	}
	if finalText != "Final answer from what I already saw." {
		t.Fatalf("finalText = %q, want the model's final answer", finalText)
	}
	if bashTool.calls != 4 {
		t.Fatalf("bash executions = %d, want 4 (loop guard short-circuits from the 5th repeat)", bashTool.calls)
	}
	if len(cl.functionResults) != 8 {
		t.Fatalf("SendFunctionResponse calls = %d, want 8 (4 real + 2 hints + 2 finalize)", len(cl.functionResults))
	}

	hintMsg := functionResultError(t, cl.functionResults[4])
	if !strings.Contains(hintMsg, "Do not call it again") || strings.Contains(hintMsg, "final answer") {
		t.Fatalf("5th result = %q, want a plain recovery hint (not finalize)", hintMsg)
	}
	for i := 6; i <= 7; i++ {
		msg := functionResultError(t, cl.functionResults[i])
		if !strings.Contains(msg, "final answer") {
			t.Fatalf("result #%d = %q, want force-finalize demand", i+1, msg)
		}
		if !strings.Contains(msg, "Do not call it again") {
			t.Fatalf("result #%d = %q, finalize must keep the loop-break phrase", i+1, msg)
		}
	}
}

// The hard abort survives as the bounded backstop: ignoring the hints AND the
// finalize demands still terminates the turn (quota protection).
// v0.100.100: the backstop for a recovery-safe (side-effect-free) loop is a
// GRACEFUL turn stop — honest final text, calls paired in history, nil error —
// never the dead "Agent Got Stuck" error card. The turn still ends (quota
// protected); only the form changed.
func TestExecutorExecuteLoop_ReadOnlyBashLoopGracefulStopAfterFinalize(t *testing.T) {
	registry := NewRegistry()
	bashTool := &scriptedStaticTool{name: "bash", content: "On branch main"}
	if err := registry.Register(bashTool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	const cmd = "git status --short && git log --oneline -3"
	responses := make([]*client.StreamingResponse, 0, 9)
	for i := 0; i < 9; i++ {
		responses = append(responses, buildExecutorTestBashStream(
			"c"+string(rune('0'+i)), cmd))
	}
	cl := &scriptedExecutorClient{model: "glm-5.2", responses: responses}

	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false

	history, text, err := exec.Execute(context.Background(), nil, "check repo state")
	if err != nil {
		t.Fatalf("Execute() error = %v, want graceful stop (nil) for a side-effect-free loop", err)
	}
	if !strings.Contains(text, "Loop guard stopped this turn") {
		t.Fatalf("final text = %q, want the honest loop-guard stop note", text)
	}
	if bashTool.calls != 4 {
		t.Fatalf("bash executions = %d, want 4", bashTool.calls)
	}
	if len(cl.functionResults) != 8 {
		t.Fatalf("SendFunctionResponse calls = %d, want 8 (4 real + 2 hints + 2 finalize) before the stop", len(cl.functionResults))
	}
	// The final batch must be PAIRED in history (no orphaned tool_use).
	if len(history) == 0 {
		t.Fatal("history is empty")
	}
	last := history[len(history)-1]
	paired := false
	for _, p := range last.Parts {
		if p.FunctionResponse != nil {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("last history entry must carry the pairing tool results, got role=%v", last.Role)
	}
}

// functionResultError extracts the error message from a single-call recovery
// batch replacement.
func functionResultError(t *testing.T, results []*genai.FunctionResponse) string {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	if success, _ := results[0].Response["success"].(bool); success {
		t.Fatalf("recovery result should be marked unsuccessful: %+v", results[0].Response)
	}
	msg, _ := results[0].Response["error"].(string)
	return msg
}
