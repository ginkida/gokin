package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCoordinateTool_MaxParallelIsAppliedAndClamped(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested float64
		want      int
	}{
		{name: "requested limit", requested: 2, want: 2},
		{name: "minimum", requested: 0, want: 1},
		{name: "task count", requested: 100, want: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			fc := &fakeCoordinator{waitResults: map[string]any{
				"internal-1": map[string]any{"status": "completed"},
				"internal-2": map[string]any{"status": "completed"},
				"internal-3": map[string]any{"status": "completed"},
			}}
			tool := NewCoordinateTool()
			tool.SetCoordinatorFactory(func() any { return fc })

			result, err := tool.Execute(context.Background(), map[string]any{
				"tasks":        tasksArg("a", "b", "c"),
				"max_parallel": test.requested,
			})
			if err != nil || !result.Success {
				t.Fatalf("Execute failed: result=%+v err=%v", result, err)
			}
			if fc.maxParallel != test.want {
				t.Fatalf("SetMaxParallel received %d, want %d", fc.maxParallel, test.want)
			}
		})
	}
}

func TestCoordinateTool_OmittedMaxParallelKeepsFactoryDefault(t *testing.T) {
	fc := &fakeCoordinator{waitResults: map[string]any{
		"internal-1": map[string]any{"status": "completed"},
	}}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	_, _ = tool.Execute(context.Background(), map[string]any{"tasks": tasksArg("a")})
	if fc.maxParallel != 0 {
		t.Fatalf("SetMaxParallel was called for an omitted option: got %d", fc.maxParallel)
	}
}

func TestCoordinateTool_TimeoutOptionMatchesExecutorBudget(t *testing.T) {
	fc := &fakeCoordinator{waitResults: map[string]any{
		"internal-1": map[string]any{"status": "completed"},
	}}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks":           tasksArg("a"),
		"timeout_minutes": 30,
	})
	if err != nil || !result.Success {
		t.Fatalf("Execute failed: result=%+v err=%v", result, err)
	}
	if fc.waitTimeout != 30*time.Minute {
		t.Fatalf("coordinator wait timeout = %v, want 30m", fc.waitTimeout)
	}
	wantOuter := 30*time.Minute + coordinateCleanupTimeout + toolTimeoutCompletionGrace
	if got := toolExecutionTimeout(2*time.Minute, 0, false, "coordinate",
		map[string]any{"timeout_minutes": 30}); got != wantOuter {
		t.Fatalf("executor coordinate timeout = %v, want %v", got, wantOuter)
	}
}

func TestCoordinateTool_ImplicitWaitUsesRaisedExecutorBudget(t *testing.T) {
	fc := &fakeCoordinator{waitResults: map[string]any{
		"internal-1": map[string]any{"status": "completed"},
	}}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	outer := 46*time.Minute + coordinateCleanupTimeout + toolTimeoutCompletionGrace
	ctx, cancel := context.WithTimeout(context.Background(), outer)
	defer cancel()
	result, err := tool.Execute(ctx, map[string]any{"tasks": tasksArg("a")})
	if err != nil || !result.Success {
		t.Fatalf("Execute failed: result=%+v err=%v", result, err)
	}
	want := 46 * time.Minute
	if delta := want - fc.waitTimeout; delta < 0 || delta > time.Second {
		t.Fatalf("implicit coordinator wait timeout = %v, want approximately %v", fc.waitTimeout, want)
	}
}

func TestCoordinateTool_RejectsFractionalTimeout(t *testing.T) {
	tool := NewCoordinateTool()
	err := tool.Validate(map[string]any{
		"tasks":           tasksArg("a"),
		"timeout_minutes": 1.5,
	})
	if err == nil || !strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("Validate fractional timeout error = %v", err)
	}
}

func TestCoordinateTool_NonSuccessfulStatusIsNeverReportedCompleted(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", ""} {
		t.Run(status, func(t *testing.T) {
			fc := &fakeCoordinator{waitResults: map[string]any{
				"internal-1": map[string]any{"status": status, "output": "partial output"},
			}}
			tool := NewCoordinateTool()
			tool.SetCoordinatorFactory(func() any { return fc })

			result, err := tool.Execute(context.Background(), map[string]any{"tasks": tasksArg("a")})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if strings.Contains(result.Content, "Status: **Completed**") {
				t.Fatalf("status %q was reported completed:\n%s", status, result.Content)
			}
			if !strings.Contains(result.Content, "1 failed") {
				t.Fatalf("status %q missing from failed count:\n%s", status, result.Content)
			}
		})
	}
}
