package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeCoordinator implements the unexported coordinatorInterface CoordinateTool.Execute
// asserts against (structural typing — no shared interface type needed).
type fakeCoordinator struct {
	nextID      int
	waitResults map[string]any
	waitErr     error
	stopped     int
	addCalls    []fakeCoordinateAddCall
	maxParallel int
}

type fakeCoordinateAddCall struct {
	prompt string
	deps   []string
}

func (f *fakeCoordinator) AddTask(prompt string, agentType any, priority any, deps []string) string {
	f.nextID++
	f.addCalls = append(f.addCalls, fakeCoordinateAddCall{prompt: prompt, deps: append([]string(nil), deps...)})
	return fmt.Sprintf("internal-%d", f.nextID)
}
func (f *fakeCoordinator) Start() {}
func (f *fakeCoordinator) SetMaxParallel(maxParallel int) {
	f.maxParallel = maxParallel
}
func (f *fakeCoordinator) WaitWithTimeout(ctx context.Context, timeout time.Duration) (map[string]any, error) {
	return f.waitResults, f.waitErr
}
func (f *fakeCoordinator) GetStatus() any { return nil }
func (f *fakeCoordinator) Stop()          { f.stopped++ }

// Execute must always Stop() the coordinator it created (defer), so a per-call
// coordinator built from the app-lifetime context doesn't leak a context node.
func TestCoordinateTool_ExecuteStopsCoordinator(t *testing.T) {
	fc := &fakeCoordinator{waitResults: map[string]any{"internal-1": map[string]any{"status": "completed", "output": "ok"}}}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })
	if _, err := tool.Execute(context.Background(), map[string]any{"tasks": tasksArg("a")}); err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if fc.stopped != 1 {
		t.Fatalf("Execute must Stop() the coordinator exactly once; got %d", fc.stopped)
	}
}

func tasksArg(ids ...string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{
			"id":         id,
			"prompt":     "do " + id,
			"agent_type": "general",
		})
	}
	return out
}

// TestCoordinateTool_TimeoutSurfacesPartialResults pins the fix: when
// WaitWithTimeout returns a non-nil error ALONGSIDE some completed results
// (the coordinator's partial-results-on-timeout fix), Execute must render
// the completed work and list the incomplete tasks, not discard everything
// behind a bare "coordination failed" error.
func TestCoordinateTool_TimeoutSurfacesPartialResults(t *testing.T) {
	fc := &fakeCoordinator{
		waitResults: map[string]any{
			// internal-1 corresponds to task "a" (first AddTask call).
			"internal-1": map[string]any{"status": "completed", "output": "did task a"},
			// "b" has no entry — still running when the timeout fired.
		},
		waitErr: fmt.Errorf("coordination timed out after %v", time.Minute),
	}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": tasksArg("a", "b"),
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() should still succeed with partial results, got Error=%q", result.Error)
	}
	if !strings.Contains(result.Content, "did task a") {
		t.Errorf("completed task's output missing from content:\n%s", result.Content)
	}
	if !strings.Contains(result.Content, "b") || !strings.Contains(result.Content, "did not finish") {
		t.Errorf("incomplete task 'b' not flagged as did-not-finish:\n%s", result.Content)
	}
}

// TestCoordinateTool_TimeoutWithNoResultsStillErrors: nothing finished at
// all — the bare error is the only useful information, matching pre-fix
// behavior for the genuinely-empty case.
func TestCoordinateTool_TimeoutWithNoResultsStillErrors(t *testing.T) {
	fc := &fakeCoordinator{
		waitResults: nil,
		waitErr:     fmt.Errorf("coordination timed out after %v", time.Minute),
	}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": tasksArg("a"),
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Success {
		t.Error("Execute() with zero completed results on timeout should report failure")
	}
	if !strings.Contains(result.Error, "coordination failed") {
		t.Errorf("expected the bare coordination-failed error, got %q", result.Error)
	}
}

// TestCoordinateTool_NormalCompletionUnaffected: the happy path (no error)
// keeps its original rendering.
func TestCoordinateTool_NormalCompletionUnaffected(t *testing.T) {
	fc := &fakeCoordinator{
		waitResults: map[string]any{
			"internal-1": map[string]any{"status": "completed", "output": "all done"},
		},
	}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": tasksArg("a"),
	})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() should succeed, got Error=%q", result.Error)
	}
	if !strings.Contains(result.Content, "## Coordination Complete") {
		t.Errorf("expected the normal-completion header, got:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "did not finish") {
		t.Errorf("normal completion must not mention incomplete tasks:\n%s", result.Content)
	}
}

func TestCoordinateTool_PropagatesSubagentPolicyBlock(t *testing.T) {
	fc := &fakeCoordinator{waitResults: map[string]any{
		"internal-1": map[string]any{
			"status": "completed",
			"output": "subagent explained the refusal",
			"policy_block": map[string]any{
				"kind":   string(PolicyBlockHook),
				"reason": "trusted hook denied the write",
			},
		},
	}}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	result, err := tool.Execute(context.Background(), map[string]any{"tasks": tasksArg("a")})
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("coordination protocol should still return its summary: %+v", result)
	}
	if result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockHook {
		t.Fatalf("subagent policy refusal was lost: %#v", result.PolicyBlock)
	}
}

func (f *fakeCoordinator) CancelRunning() int { return 0 }
