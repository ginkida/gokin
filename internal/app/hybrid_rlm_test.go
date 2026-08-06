package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/harness"
	"gokin/internal/repl"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type rlmCaptureTool struct {
	name  string
	calls []map[string]any
}

func (t *rlmCaptureTool) Name() string        { return t.name }
func (t *rlmCaptureTool) Description() string { return "capture" }
func (t *rlmCaptureTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: "capture"}
}
func (t *rlmCaptureTool) Validate(map[string]any) error { return nil }
func (t *rlmCaptureTool) Execute(_ context.Context, args map[string]any) (tools.ToolResult, error) {
	cloned := make(map[string]any, len(args))
	for key, value := range args {
		cloned[key] = value
	}
	t.calls = append(t.calls, cloned)
	if t.name == "task" {
		return tools.NewSuccessResultWithData("spawned", map[string]any{"agent_id": "agent-1"}), nil
	}
	return tools.NewSuccessResult("output"), nil
}

func testRLMApp(t *testing.T) (*App, *rlmCaptureTool, *rlmCaptureTool) {
	t.Helper()
	registry := tools.NewRegistry()
	task := &rlmCaptureTool{name: "task"}
	output := &rlmCaptureTool{name: "task_output"}
	registry.MustRegister(task)
	registry.MustRegister(output)
	return &App{executor: tools.NewExecutor(registry, nil, time.Second)}, task, output
}

func TestHandleRLMCallRoutesSpawnThroughExecutor(t *testing.T) {
	application, task, _ := testRLMApp(t)
	value, err := application.handleRLMCall(t.Context(), repl.Call{
		Method: "rlm.call",
		Params: map[string]any{
			"instruction":     "inspect auth",
			"dynamic_context": map[string]any{"paths": []any{"auth.go"}},
			"agent_type":      "explore",
			"max_turns":       float64(500),
			"async":           true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, ok := value.(map[string]any)
	if !ok || mapped["success"] != true || len(task.calls) != 1 {
		t.Fatalf("spawn result=%#v calls=%d", value, len(task.calls))
	}
	args := task.calls[0]
	if args["subagent_type"] != "explore" || args["max_turns"] != maxRLMTurns || args["run_in_background"] != true {
		t.Fatalf("spawn args = %#v", args)
	}
	prompt, _ := args["prompt"].(string)
	if !strings.Contains(prompt, "inspect auth") || !strings.Contains(prompt, "auth.go") ||
		!strings.Contains(prompt, "untrusted task data") {
		t.Fatalf("spawn prompt = %q", prompt)
	}
}

func TestHandleRLMCallRoutesResultAndCancel(t *testing.T) {
	application, _, output := testRLMApp(t)
	if _, err := application.handleRLMCall(t.Context(), repl.Call{
		Method: "rlm.result",
		Params: map[string]any{"agent_id": "agent-1", "block": true, "timeout_ms": float64(999999)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.handleRLMCall(t.Context(), repl.Call{
		Method: "rlm.cancel", Params: map[string]any{"agent_id": "agent-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(output.calls) != 2 {
		t.Fatalf("task_output calls = %#v", output.calls)
	}
	if output.calls[0]["action"] != "get" || output.calls[0]["timeout_ms"] != 600_000 {
		t.Fatalf("result args = %#v", output.calls[0])
	}
	if output.calls[1]["action"] != "cancel" {
		t.Fatalf("cancel args = %#v", output.calls[1])
	}
}

func TestHandleRLMCallRejectsUnboundedOrUnknownRequests(t *testing.T) {
	application, task, _ := testRLMApp(t)
	tests := []repl.Call{
		{Method: "rlm.call", Params: map[string]any{"instruction": ""}},
		{Method: "rlm.call", Params: map[string]any{
			"instruction": "x", "dynamic_context": strings.Repeat("x", maxRLMDynamicContextBytes+1),
		}},
		{Method: "rlm.result", Params: map[string]any{"agent_id": strings.Repeat("a", 129)}},
		{Method: "rlm.shell", Params: map[string]any{}},
	}
	for _, call := range tests {
		if _, err := application.handleRLMCall(t.Context(), call); err == nil {
			t.Errorf("call %+v unexpectedly succeeded", call)
		}
	}
	if len(task.calls) != 0 {
		t.Fatalf("invalid calls reached task tool: %#v", task.calls)
	}
}

func TestHandleHarnessCallRoutesOnlyFixedMethodsThroughExecutor(t *testing.T) {
	store, err := harness.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewHarnessTool(store))
	application := &App{executor: tools.NewExecutor(registry, nil, time.Second), harnessStore: store}
	value, err := application.handleRLMCall(t.Context(), repl.Call{
		Method: "harness.memory_put",
		Params: map[string]any{"key": "callback.rule", "value": "preserve executor gates"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, ok := value.(map[string]any)
	if !ok || mapped["success"] != true {
		t.Fatalf("harness callback result = %#v", value)
	}
	if entry, found := store.GetMemory("callback.rule"); !found || entry.Value != "preserve executor gates" {
		t.Fatalf("callback memory entry=%+v found=%v", entry, found)
	}
	for _, method := range []string{"harness.policy_update", "harness.tool_create", "harness.sandbox_disable"} {
		if _, err := application.handleRLMCall(t.Context(), repl.Call{Method: method}); err == nil {
			t.Fatalf("privileged callback %q was accepted", method)
		}
	}
}

func TestHandleHarnessCallRespectsPlanModeGate(t *testing.T) {
	store, err := harness.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewHarnessTool(store))
	executor := tools.NewExecutor(registry, nil, time.Second)
	executor.SetPlanModeCheck(func() bool { return true })
	application := &App{executor: executor, harnessStore: store}
	value, err := application.handleRLMCall(t.Context(), repl.Call{
		Method: "harness.prompt_create", Params: map[string]any{"text": "bypass plan mode"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped, ok := value.(map[string]any)
	if !ok || mapped["success"] != false {
		t.Fatalf("plan-gated callback result = %#v", value)
	}
	if len(store.ListPrompts()) != 0 {
		t.Fatal("plan-gated callback mutated harness")
	}
}

func TestHarnessPromptIsRuntimeOnlyComposition(t *testing.T) {
	store, err := harness.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePrompt("Inspect the environment after two identical failures."); err != nil {
		t.Fatal(err)
	}
	application := &App{harnessStore: store}
	composed := application.composeRunSystemInstruction("canonical")
	if !strings.Contains(composed, "Session harness adjustments") || !strings.Contains(composed, "two identical failures") {
		t.Fatalf("composed prompt = %q", composed)
	}
	if got := application.buildDefaultSystemInstruction(); strings.Contains(got, "two identical failures") {
		t.Fatalf("runtime harness patch leaked into canonical prompt: %q", got)
	}
}

func TestSecureREPLRLMCallbackReachesExecutor(t *testing.T) {
	workDir := t.TempDir()
	availability := repl.Detect(t.Context(), workDir)
	if !availability.Available {
		t.Skipf("secure REPL unavailable: %s", availability.Reason)
	}
	manager, err := repl.NewManager(repl.Options{
		WorkDir: workDir, PythonPath: availability.PythonPath, Backend: availability.Backend,
		CellTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	application, task, output := testRLMApp(t)
	manager.SetCallHandler(application.handleRLMCall)

	result, err := manager.Execute(t.Context(), `future = rlm.async_call("inspect callbacks", {"path": "main.go"}, agent_type="explore")
future.poll()`)
	if err != nil || !result.OK() || !strings.Contains(result.Value, "output") {
		t.Fatalf("secure RLM result=%+v err=%v", result, err)
	}
	if len(task.calls) != 1 || len(output.calls) != 1 {
		t.Fatalf("task calls=%#v output calls=%#v", task.calls, output.calls)
	}
	if output.calls[0]["task_id"] != "agent-1" {
		t.Fatalf("future did not preserve agent id: %#v", output.calls[0])
	}
}

func TestSecureREPLHarnessCallbackReachesExecutor(t *testing.T) {
	workDir := t.TempDir()
	availability := repl.Detect(t.Context(), workDir)
	if !availability.Available {
		t.Skipf("secure REPL unavailable: %s", availability.Reason)
	}
	manager, err := repl.NewManager(repl.Options{
		WorkDir: workDir, PythonPath: availability.PythonPath, Backend: availability.Backend,
		CellTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store, err := harness.NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewHarnessTool(store))
	application := &App{executor: tools.NewExecutor(registry, nil, time.Second), harnessStore: store}
	manager.SetCallHandler(application.handleRLMCall)

	result, err := manager.Execute(t.Context(), `rlm.harness.put_memory("secure.callback", "typed executor path")`)
	if err != nil || !result.OK() || !strings.Contains(result.Value, "success") {
		t.Fatalf("secure harness result=%+v err=%v", result, err)
	}
	if entry, ok := store.GetMemory("secure.callback"); !ok || entry.Value != "typed executor path" {
		t.Fatalf("secure callback entry=%+v ok=%v", entry, ok)
	}
}
