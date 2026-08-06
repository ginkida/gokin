package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/repl"
)

type fakeREPLExecutor struct {
	result repl.Result
	err    error
	code   string
	resets int
	stats  repl.Stats
}

func (f *fakeREPLExecutor) Reset(context.Context) error { f.resets++; return f.err }
func (f *fakeREPLExecutor) Stats() repl.Stats           { return f.stats }

func (f *fakeREPLExecutor) Execute(_ context.Context, code string) (repl.Result, error) {
	f.code = code
	return f.result, f.err
}

func TestReplExecToolReturnsStructuredPersistentResult(t *testing.T) {
	fake := &fakeREPLExecutor{result: repl.Result{
		Generation: 3,
		Stdout:     "loaded\n",
		Value:      "{'matches': 12}",
		Artifact:   &repl.ArtifactRef{ID: "art_1", Size: 70000},
		Truncated:  true,
	}}
	tool := NewReplExecTool(fake)
	result, err := tool.Execute(t.Context(), map[string]any{"code": "matches = []\nlen(matches)"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || fake.code == "" {
		t.Fatalf("result=%+v code=%q", result, fake.code)
	}
	for _, want := range []string{"generation: 3", "loaded", "matches", "art_1", "bounded"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("content missing %q: %s", want, result.Content)
		}
	}
	if _, ok := result.Data.(repl.Result); !ok {
		t.Fatalf("data type = %T, want repl.Result", result.Data)
	}
}

func TestReplExecToolSurfacesPythonErrorWithPartialOutput(t *testing.T) {
	tool := NewReplExecTool(&fakeREPLExecutor{result: repl.Result{
		Generation: 2,
		Stdout:     "before failure\n",
		Error: &repl.ExecutionError{
			Type: "ValueError", Message: "bad value", Traceback: "trace",
		},
	}})
	result, err := tool.Execute(t.Context(), map[string]any{"code": "raise ValueError()"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "ValueError") ||
		!strings.Contains(result.Content, "before failure") {
		t.Fatalf("result = %+v", result)
	}
}

func TestReplExecToolUnavailableAndRuntimeFailure(t *testing.T) {
	unavailable, _ := NewReplExecTool(nil).Execute(t.Context(), map[string]any{"code": "1"})
	if unavailable.Success || !strings.Contains(unavailable.Error, "unavailable") {
		t.Fatalf("unavailable result = %+v", unavailable)
	}

	runtimeFailure, _ := NewReplExecTool(&fakeREPLExecutor{
		err: errors.New("kernel died"), stats: repl.Stats{Generation: 2, TransportFailures: 1},
	}).Execute(
		t.Context(), map[string]any{"code": "1"})
	if runtimeFailure.Success || !strings.Contains(runtimeFailure.Error, "kernel died") ||
		!strings.Contains(runtimeFailure.Content, "clean generation") {
		t.Fatalf("runtime failure = %+v", runtimeFailure)
	}
}

func TestReplExecToolContractAndClassification(t *testing.T) {
	tool := NewReplExecTool(nil)
	if err := tool.Validate(nil); err == nil {
		t.Fatal("Validate(nil) succeeded")
	}
	if err := tool.Validate(map[string]any{"code": "   "}); err == nil {
		t.Fatal("Validate(blank) succeeded")
	}
	decl := tool.Declaration()
	if decl.Name != "repl_exec" || len(decl.Parameters.Required) != 0 {
		t.Fatalf("declaration = %+v", decl)
	}
	if IsParallelSafeTool("repl_exec") {
		t.Fatal("stateful REPL must be sequential")
	}
	if IsWriteTool("repl_exec") {
		t.Fatal("read-only REPL must not be classified as a mutation")
	}
	if !IsReadOnlyForPlanMode("repl_exec") {
		t.Fatal("read-only REPL should remain available in plan mode")
	}
}

func TestReplExecToolStatusAndReset(t *testing.T) {
	fake := &fakeREPLExecutor{stats: repl.Stats{
		Generation: 4, Running: true, Restarts: 3, Executions: 9, TransportFailures: 2, Timeouts: 1,
	}}
	tool := NewReplExecTool(fake)
	status, err := tool.Execute(t.Context(), map[string]any{"action": "status"})
	if err != nil || !status.Success || !strings.Contains(status.Content, "generation=4") || !strings.Contains(status.Content, "timeouts=1") {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	reset, err := tool.Execute(t.Context(), map[string]any{"action": "reset"})
	if err != nil || !reset.Success || fake.resets != 1 {
		t.Fatalf("reset=%+v err=%v resets=%d", reset, err, fake.resets)
	}
	if err := tool.Validate(map[string]any{"action": "unknown"}); err == nil {
		t.Fatal("unknown action validated")
	}
}

func TestCloneRegistryDoesNotShareForegroundREPL(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(NewReplExecTool(&fakeREPLExecutor{}))
	cloned := CloneRegistryForWorkDir(registry, t.TempDir())
	if _, ok := cloned.Get("repl_exec"); ok {
		t.Fatal("cloned sub-agent registry retained foreground REPL")
	}
}

func TestExecutorInvokeToolPreservesCapabilityCeiling(t *testing.T) {
	registry := NewRegistry()
	read := &scriptedStaticTool{name: "read", content: "proof"}
	registry.MustRegister(read)
	executor := NewExecutor(registry, nil, time.Second)

	allowedCtx := ContextWithToolCapabilityCeiling(t.Context(), []string{"read"})
	allowed, err := executor.InvokeTool(allowedCtx, "read", map[string]any{"path": "x"})
	if err != nil || !allowed.Success || allowed.Content != "proof" || read.calls != 1 {
		t.Fatalf("allowed nested invocation = %+v, err=%v, calls=%d", allowed, err, read.calls)
	}

	blockedCtx := ContextWithToolCapabilityCeiling(t.Context(), []string{})
	blocked, err := executor.InvokeTool(blockedCtx, "read", nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Success || blocked.PolicyBlock == nil || blocked.PolicyBlock.Kind != PolicyBlockPermission {
		t.Fatalf("blocked nested invocation = %+v", blocked)
	}
	if read.calls != 1 {
		t.Fatalf("blocked invocation executed tool; calls=%d", read.calls)
	}
}

func TestReplExecOuterBudgetLeavesRoomForSynchronousDelegation(t *testing.T) {
	got := toolExecutionTimeout(2*time.Minute, 0, false, "repl_exec", nil)
	want := config.DefaultThoroughAgentTimeout + toolTimeoutCompletionGrace
	if got != want {
		t.Fatalf("repl_exec timeout = %v, want %v", got, want)
	}
}
