package tools

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genai"
)

// hangingTool models a misbehaving tool that IGNORES context cancellation and
// blocks indefinitely (a blocking syscall, a non-context-aware network read, a
// deadlocked mutex). It is the exact failure mode the #7 backstop must contain.
type hangingTool struct {
	started chan struct{}
	release chan struct{}
}

func (h *hangingTool) Name() string                  { return "hanging_tool" }
func (h *hangingTool) Description() string           { return "test-only: ignores ctx and blocks" }
func (h *hangingTool) Validate(map[string]any) error { return nil }
func (h *hangingTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "hanging_tool", Description: "blocks ignoring ctx"}
}
func (h *hangingTool) Execute(ctx context.Context, _ map[string]any) (ToolResult, error) {
	close(h.started)
	<-h.release // deliberately does NOT select on ctx — released only at test cleanup
	return NewSuccessResult("late"), nil
}

// TestExecutorDoExecuteTool_HungToolDoesNotBlockTurn pins the v0.98.x #7 fix:
// a tool that ignores ctx cancellation must not hang the whole turn. The
// executor runs Execute in a goroutine and selects on execCtx, so it returns a
// timeout result within ~toolTimeout+grace even though the tool goroutine never
// returns. Before the fix, doExecuteTool blocked forever and only Ctrl+C escaped.
func TestExecutorDoExecuteTool_HungToolDoesNotBlockTurn(t *testing.T) {
	registry := NewRegistry()
	tool := &hangingTool{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(tool.release) })
	if err := registry.Register(tool); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	cl := &scriptedExecutorClient{model: "glm-5.1"}
	exec := NewExecutor(registry, cl, 150*time.Millisecond) // short per-tool timeout

	call := &genai.FunctionCall{ID: "h1", Name: "hanging_tool", Args: map[string]any{}}

	done := make(chan ToolResult, 1)
	go func() { done <- exec.doExecuteTool(context.Background(), call) }()

	select {
	case res := <-done:
		// Must have started (we exercised the hang path, not a pre-exec reject).
		select {
		case <-tool.started:
		default:
			t.Fatal("hanging tool never started — test did not exercise the backstop")
		}
		if res.Success {
			t.Errorf("expected a timeout failure for a ctx-ignoring tool, got success: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("doExecuteTool hung on a ctx-ignoring tool — #7 backstop failed")
	}
}
