package tools

import (
	"context"
	"strings"
	"testing"
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
