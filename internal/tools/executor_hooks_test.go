package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"gokin/internal/hooks"
)

func newHooksExecutor(t *testing.T, hook *hooks.Hook) (*Executor, *scriptedStaticTool) {
	t.Helper()
	registry := NewRegistry()
	tool := &scriptedStaticTool{name: "write", content: "written"}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register: %v", err)
	}
	exec := NewExecutor(registry, nil, time.Second)

	mgr := hooks.NewManager(true, t.TempDir())
	mgr.AddHook(hook)
	exec.SetHooks(mgr)
	return exec, tool
}

func TestPreToolHook_FailOnErrorBlocksTheCall(t *testing.T) {
	exec, tool := newHooksExecutor(t, &hooks.Hook{
		Name:        "guard",
		Type:        hooks.PreTool,
		ToolName:    "write",
		Command:     "echo 'writes are frozen today' >&2; exit 1",
		Enabled:     true,
		FailOnError: true,
	})

	result := exec.doExecuteTool(context.Background(), testFunctionCall("w1", "write", map[string]any{
		"file_path": "x.txt", "content": "data",
	}))

	if result.Success {
		t.Fatal("blocking pre-tool hook must refuse the call")
	}
	if tool.calls != 0 {
		t.Fatalf("tool executed %d times despite the block, want 0", tool.calls)
	}
	for _, needle := range []string{"hook blocked:", "guard", "writes are frozen today"} {
		if !strings.Contains(result.Error, needle) {
			t.Fatalf("block reason missing %q: %q", needle, result.Error)
		}
	}
	if isExecutionFailure(result.Error) {
		t.Fatal("hook block must not count as a circuit-breaker failure")
	}
}

func TestPreToolHook_NonBlockingFailureLetsToolRun(t *testing.T) {
	exec, tool := newHooksExecutor(t, &hooks.Hook{
		Name:     "advisory",
		Type:     hooks.PreTool,
		ToolName: "write",
		Command:  "exit 1",
		Enabled:  true,
		// FailOnError false: hook failures must never break the turn.
	})

	result := exec.doExecuteTool(context.Background(), testFunctionCall("w2", "write", map[string]any{
		"file_path": "y.txt", "content": "data",
	}))

	if !result.Success {
		t.Fatalf("advisory hook failure must not block: %s", result.Error)
	}
	if tool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls)
	}
}

func TestPreToolHook_MatcherScopesBlocking(t *testing.T) {
	exec, tool := newHooksExecutor(t, &hooks.Hook{
		Name:        "bash-only",
		Type:        hooks.PreTool,
		ToolName:    "bash",
		Command:     "exit 1",
		Enabled:     true,
		FailOnError: true,
	})

	result := exec.doExecuteTool(context.Background(), testFunctionCall("w3", "write", map[string]any{
		"file_path": "z.txt", "content": "data",
	}))

	if !result.Success || tool.calls != 1 {
		t.Fatalf("hook scoped to bash must not affect write: success=%v calls=%d", result.Success, tool.calls)
	}
}

func testFunctionCall(id, name string, args map[string]any) *genai.FunctionCall {
	return &genai.FunctionCall{ID: id, Name: name, Args: args}
}
