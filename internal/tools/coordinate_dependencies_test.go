package tools

import (
	"context"
	"strings"
	"testing"
)

func coordinateTask(id string, dependencies ...string) map[string]any {
	task := map[string]any{
		"id":         id,
		"prompt":     "do " + id,
		"agent_type": "general",
	}
	if len(dependencies) > 0 {
		deps := make([]any, len(dependencies))
		for i, dependency := range dependencies {
			deps[i] = dependency
		}
		task["depends_on"] = deps
	}
	return task
}

func TestCoordinateTool_ForwardDependencyIsPreserved(t *testing.T) {
	fc := &fakeCoordinator{waitResults: map[string]any{
		"internal-1": map[string]any{"status": "completed", "output": "build done"},
		"internal-2": map[string]any{"status": "completed", "output": "deploy done"},
	}}
	tool := NewCoordinateTool()
	tool.SetCoordinatorFactory(func() any { return fc })

	// "deploy" intentionally appears before its prerequisite. The old
	// single-pass mapper did not know build's internal ID yet and silently
	// submitted deploy with no dependencies.
	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			coordinateTask("deploy", "build"),
			coordinateTask("build"),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute failed: %s", result.Error)
	}
	if len(fc.addCalls) != 2 {
		t.Fatalf("AddTask calls = %d, want 2", len(fc.addCalls))
	}
	if fc.addCalls[0].prompt != "do build" {
		t.Fatalf("first submitted task = %q, want prerequisite build", fc.addCalls[0].prompt)
	}
	if fc.addCalls[1].prompt != "do deploy" {
		t.Fatalf("second submitted task = %q, want dependent deploy", fc.addCalls[1].prompt)
	}
	if len(fc.addCalls[1].deps) != 1 || fc.addCalls[1].deps[0] != "internal-1" {
		t.Fatalf("deploy dependencies = %v, want [internal-1]", fc.addCalls[1].deps)
	}
}

func TestCoordinateTool_ValidateRejectsDependencyCycle(t *testing.T) {
	tool := NewCoordinateTool()
	err := tool.Validate(map[string]any{
		"tasks": []any{
			coordinateTask("a", "b"),
			coordinateTask("b", "c"),
			coordinateTask("c", "a"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("Validate error = %v, want dependency cycle", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("cycle error %q does not identify task %q", err, id)
		}
	}
}

func TestCoordinateTool_ExecuteRejectsCycleBeforeCreatingCoordinator(t *testing.T) {
	tool := NewCoordinateTool()
	factoryCalls := 0
	tool.SetCoordinatorFactory(func() any {
		factoryCalls++
		return &fakeCoordinator{}
	})

	result, err := tool.Execute(context.Background(), map[string]any{
		"tasks": []any{
			coordinateTask("a", "b"),
			coordinateTask("b", "a"),
		},
	})
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "dependency cycle") {
		t.Fatalf("Execute result = %+v, want cycle validation failure", result)
	}
	if factoryCalls != 0 {
		t.Fatalf("coordinator factory called %d times for invalid graph, want 0", factoryCalls)
	}
}

func TestCoordinateTool_ValidateRejectsMalformedDependencies(t *testing.T) {
	tool := NewCoordinateTool()
	task := coordinateTask("a")
	task["depends_on"] = []any{"", 42}
	err := tool.Validate(map[string]any{"tasks": []any{task}})
	if err == nil || !strings.Contains(err.Error(), "non-empty task IDs") {
		t.Fatalf("Validate error = %v, want malformed dependency error", err)
	}
}
