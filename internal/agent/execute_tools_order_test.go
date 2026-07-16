package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type orderingWriteTool struct{ wrote *atomic.Bool }

func (t *orderingWriteTool) Name() string        { return "write" }
func (t *orderingWriteTool) Description() string { return "ordering regression write" }
func (t *orderingWriteTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *orderingWriteTool) Validate(map[string]any) error { return nil }
func (t *orderingWriteTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.wrote.Store(true)
	return tools.NewSuccessResult("written"), nil
}

type orderingReadTool struct{ wrote *atomic.Bool }

func (t *orderingReadTool) Name() string        { return "read" }
func (t *orderingReadTool) Description() string { return "ordering regression read" }
func (t *orderingReadTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *orderingReadTool) Validate(map[string]any) error { return nil }
func (t *orderingReadTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	if !t.wrote.Load() {
		return tools.NewErrorResult("read ran before preceding write"), nil
	}
	return tools.NewSuccessResult("observed write"), nil
}

func TestExecuteTools_PreservesWriteThenReadOrder(t *testing.T) {
	var wrote atomic.Bool
	registry := tools.NewRegistry()
	if err := registry.Register(&orderingWriteTool{wrote: &wrote}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(&orderingReadTool{wrote: &wrote}); err != nil {
		t.Fatal(err)
	}
	a := &Agent{registry: registry}

	results := a.executeTools(context.Background(), []*genai.FunctionCall{
		{ID: "write-1", Name: "write", Args: map[string]any{}},
		{ID: "read-1", Name: "read", Args: map[string]any{}},
	})

	if len(results) != 2 || results[1].Response == nil {
		t.Fatalf("results = %#v", results)
	}
	if success, _ := results[1].Response.Response["success"].(bool); !success {
		t.Fatalf("read did not observe preceding write: %#v", results[1].Response.Response)
	}
}

type cancellationBlockingTool struct {
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (t *cancellationBlockingTool) Name() string        { return "read" }
func (t *cancellationBlockingTool) Description() string { return "context-ignoring test tool" }
func (t *cancellationBlockingTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *cancellationBlockingTool) Validate(map[string]any) error { return nil }
func (t *cancellationBlockingTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	close(t.started)
	<-t.release
	close(t.returned)
	return tools.NewSuccessResult("late result"), nil
}

func TestExecuteTools_SequentialToolCannotBlockCancellationForever(t *testing.T) {
	originalGrace := sequentialToolCleanupGrace
	sequentialToolCleanupGrace = 20 * time.Millisecond
	defer func() { sequentialToolCleanupGrace = originalGrace }()

	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	registry := tools.NewRegistry()
	if err := registry.Register(&cancellationBlockingTool{started: started, release: release, returned: returned}); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	a := &Agent{registry: registry, workDir: workDir}
	hookManager := hooks.NewManager(true, workDir)
	hookManager.AddHook(&hooks.Hook{
		Name: "late-post-read", Type: hooks.PostTool, ToolName: "read",
		Command: "touch .late-post-hook", Enabled: true,
	})
	a.SetHooks(hookManager)
	var lateEnd atomic.Int32
	a.SetOnToolActivity(func(_ string, _ string, _ map[string]any, status string, _ bool, _ string) {
		if status == "end" {
			lateEnd.Add(1)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	resultsCh := make(chan []toolCallResult, 1)
	go func() {
		resultsCh <- a.executeTools(ctx, []*genai.FunctionCall{{ID: "read-1", Name: "read", Args: map[string]any{}}})
	}()

	<-started
	start := time.Now()
	cancel()
	var results []toolCallResult
	select {
	case results = <-resultsCh:
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("sequential tool kept the agent blocked after cancellation")
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("cancellation took %v, want <= 200ms", elapsed)
	}
	if len(results) != 1 || results[0].Response == nil || results[0].Response.Response["error"] != "cancelled" {
		t.Fatalf("cancelled result = %#v", results)
	}

	// Let the abandoned goroutine exit. Its buffered result must not mutate the
	// returned slice or block while trying to report a late completion.
	close(release)
	<-returned
	time.Sleep(25 * time.Millisecond)
	if got := a.GetToolsUsed(); len(got) != 0 {
		t.Fatalf("abandoned tool polluted agent state after return: %v", got)
	}
	if lateEnd.Load() != 0 {
		t.Fatalf("abandoned tool emitted %d late end callback(s)", lateEnd.Load())
	}
	if _, err := os.Stat(filepath.Join(workDir, ".late-post-hook")); !os.IsNotExist(err) {
		t.Fatalf("abandoned tool started a late post hook: %v", err)
	}
}

type mutatingBlockingTool struct {
	started chan struct{}
	release chan struct{}
	changed *atomic.Bool
}

func (t *mutatingBlockingTool) Name() string        { return "write" }
func (t *mutatingBlockingTool) Description() string { return "non-cooperative mutating test tool" }
func (t *mutatingBlockingTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *mutatingBlockingTool) Validate(map[string]any) error { return nil }
func (t *mutatingBlockingTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	close(t.started)
	<-t.release
	t.changed.Store(true)
	return tools.NewSuccessResult("write completed"), nil
}

func TestExecuteTools_DoesNotReturnBeforeNonCooperativeMutationFinishes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var changed atomic.Bool
	registry := tools.NewRegistry()
	if err := registry.Register(&mutatingBlockingTool{started: started, release: release, changed: &changed}); err != nil {
		t.Fatal(err)
	}
	a := &Agent{registry: registry}
	ctx, cancel := context.WithCancel(context.Background())
	resultsCh := make(chan []toolCallResult, 1)
	go func() {
		resultsCh <- a.executeTools(ctx, []*genai.FunctionCall{{ID: "write-1", Name: "write", Args: map[string]any{}}})
	}()
	<-started
	cancel()

	select {
	case results := <-resultsCh:
		close(release)
		t.Fatalf("mutating tool was abandoned before its side effect completed: %#v", results)
	case <-time.After(75 * time.Millisecond):
	}
	close(release)
	results := <-resultsCh
	if !changed.Load() || len(results) != 1 || results[0].Response == nil {
		t.Fatalf("mutation/result = %v/%#v, want completion before return", changed.Load(), results)
	}
}
