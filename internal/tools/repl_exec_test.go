package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/repl"
)

func TestReplExecToolDeclarationPayloadBudget(t *testing.T) {
	declaration := NewReplExecTool(nil).Declaration()
	payload, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("repl_exec declaration JSON bytes: %d", len(payload))
	// This declaration is sent on every model round that exposes hybrid mode.
	// Pin the compact contract so explanatory prose cannot quietly restore the
	// old 4,343-byte recurring payload.
	if len(payload) > 3400 {
		t.Fatalf("repl_exec declaration exceeds 3,400-byte request budget: %d", len(payload))
	}
	for _, required := range []string{
		"context.workspace -> str property",
		"context.search_code(query",
		`"matches":[{"path","line","text"}]`,
		"context.count_code(query",
		`"matching_lines","matching_files","groups"`,
		"context.count_code_many(queries",
		"ONE inventory/read pass",
		"context.list_files(path",
		"context.file_stats(path",
		"Prefer for totals",
		"file_stats streams",
		"context.read_slice(path",
		"context.artifact_get(id",
		"context.runtime_limits()",
		"rlm(instruction",
		"rlm.async_call(...)",
		"rlm.harness",
		"Directory enumeration, direct open, reflection, dynamic code",
		"writes, processes, threads, sockets, native libraries, and Git are blocked",
		"git_status/git_diff",
	} {
		if !strings.Contains(declaration.Description, required) {
			t.Errorf("compact REPL declaration omitted %q", required)
		}
	}
}

func BenchmarkReplExecDeclarationJSON(b *testing.B) {
	tool := NewReplExecTool(nil)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := json.Marshal(tool.Declaration()); err != nil {
			b.Fatal(err)
		}
	}
}

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
		Generation:         3,
		Stdout:             "loaded\n",
		Value:              "{'matches': 12}",
		Operations:         map[string]int{"count_code_many": 1},
		FileIndexRefreshes: 1,
		Artifact:           &repl.ArtifactRef{ID: "art_1", Size: 70000},
		Truncated:          true,
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
	metadata := result.Data.(repl.Result)
	if metadata.Stdout != "" || metadata.Stderr != "" || metadata.Value != "" || metadata.Artifact == nil ||
		metadata.Operations["count_code_many"] != 1 || metadata.FileIndexRefreshes != 1 {
		t.Fatalf("data should contain metadata without duplicate payloads: %+v", metadata)
	}
	if strings.Contains(result.Content, "count_code_many") || strings.Contains(result.Content, "file_index") {
		t.Fatalf("runtime telemetry leaked into model-visible content: %s", result.Content)
	}
}

func TestReplExecToolLabelsEveryOverflowArtifact(t *testing.T) {
	tool := NewReplExecTool(&fakeREPLExecutor{result: repl.Result{
		Generation: 4,
		Artifacts: map[string]*repl.ArtifactRef{
			"value":  {ID: "art_value", Size: 90000},
			"stdout": {ID: "art_stdout", Size: 80000, Truncated: true},
		},
		Truncated: true,
	}})
	result, err := tool.Execute(t.Context(), map[string]any{"code": "1"})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, want := range []string{
		"artifact[stdout]: art_stdout", "artifact[value]: art_value", "capped", "bounded",
	} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("content missing %q: %s", want, result.Content)
		}
	}
}

func TestReplExecToolKeepsArtifactHandlesInsideOuterOutputBudget(t *testing.T) {
	tool := NewReplExecTool(&fakeREPLExecutor{result: repl.Result{
		Generation: 5,
		Stdout:     strings.Repeat("s", 8*1024),
		Stderr:     strings.Repeat("e", 8*1024),
		Value:      strings.Repeat("v", 8*1024),
		Artifacts: map[string]*repl.ArtifactRef{
			"stdout": {ID: "art_stdout", Size: 10000},
			"stderr": {ID: "art_stderr", Size: 10000},
			"value":  {ID: "art_value", Size: 10000},
		},
		Truncated: true,
	}})
	result, err := tool.Execute(t.Context(), map[string]any{"code": "1"})
	if err != nil || !result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	visible, _ := result.ToMap()["content"].(string)
	for _, want := range []string{"art_stdout", "art_stderr", "art_value", "stdout:", "stderr:", "value:"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("outer content lost %q (bytes=%d): %.500s", want, len(visible), visible)
		}
	}
	if strings.Contains(visible, "OUTPUT TRUNCATED") {
		t.Fatalf("current worker maximum should fit the outer tool budget (bytes=%d)", len(visible))
	}
	data, ok := result.ToMap()["data"].(repl.Result)
	if !ok || data.Stdout != "" || data.Stderr != "" || data.Value != "" || len(data.Artifacts) != 3 {
		t.Fatalf("structured metadata duplicated or lost payloads: %#v", result.ToMap()["data"])
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
	if _, ok := result.Data.(repl.Result); !ok {
		t.Fatalf("error data type = %T, want repl.Result", result.Data)
	}
}

func TestReplExecToolExplainsFatalResourceReset(t *testing.T) {
	tool := NewReplExecTool(&fakeREPLExecutor{result: repl.Result{
		Generation:  6,
		KernelReset: true,
		Error: &repl.ExecutionError{
			Type: "MemoryLimitExceeded", Message: "peak RSS exceeded the configured limit",
		},
	}})
	result, err := tool.Execute(t.Context(), map[string]any{"code": "payload = 'x' * 100"})
	if err != nil || result.Success || !strings.Contains(result.Error, "MemoryLimitExceeded") ||
		!strings.Contains(result.Content, "generation was discarded") {
		t.Fatalf("fatal resource result=%+v err=%v", result, err)
	}
	metadata, ok := result.Data.(repl.Result)
	if !ok || !metadata.KernelReset {
		t.Fatalf("fatal reset metadata=%#v", result.Data)
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

	secureUnavailable, _ := NewReplExecTool(&fakeREPLExecutor{
		err: fmt.Errorf("%w: sandbox denied", repl.ErrUnavailable),
	}).Execute(t.Context(), map[string]any{"code": "1"})
	if secureUnavailable.Success ||
		!strings.Contains(secureUnavailable.Content, "continue this session with structured") ||
		strings.Contains(secureUnavailable.Content, "clean generation") {
		t.Fatalf("secure unavailable result = %+v", secureUnavailable)
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
		Generation: 4, Running: true, Restarts: 3, Executions: 9, TransportFailures: 2,
		Timeouts: 1, ResourceLimitFailures: 1,
	}}
	tool := NewReplExecTool(fake)
	status, err := tool.Execute(t.Context(), map[string]any{"action": "status"})
	if err != nil || !status.Success || !strings.Contains(status.Content, "generation=4") ||
		!strings.Contains(status.Content, "timeouts=1") || !strings.Contains(status.Content, "resource_limit_failures=1") {
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
