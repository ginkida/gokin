package router

import (
	"context"
	"testing"
	"time"

	"gokin/internal/testkit"
	"gokin/internal/tools"

	genai "google.golang.org/genai"
)

// declNames flattens the function-declaration names across all tools in the
// schema into a set for easy membership assertions.
func declNames(ts []*genai.Tool) map[string]bool {
	out := make(map[string]bool)
	for _, tool := range ts {
		if tool == nil {
			continue
		}
		for _, d := range tool.FunctionDeclarations {
			if d != nil {
				out[d.Name] = true
			}
		}
	}
	return out
}

// TestRouterExecute_PlanModeKeepsMutatingToolsOut pins the round-11 fix: in plan
// mode, Router.Execute's per-request tool filtering must NOT re-add mutating
// tools to the API schema. Before the fix, Execute called SetTools(
// FilteredGeminiTools(...)) — which filters by ToolSet with no plan-mode
// awareness — or left the previously-set full set untouched, defeating plan
// mode's hard schema defense (write/edit/bash removed from the schema entirely).
func TestRouterExecute_PlanModeKeepsMutatingToolsOut(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.EnqueueText("analysis only")

	registry := tools.DefaultRegistry(t.TempDir())
	exec := tools.NewExecutor(registry, mock, time.Second)

	cfg := &RouterConfig{Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100}
	r := NewRouter(cfg, exec, nil, mock, registry, false, t.TempDir())

	// Simulate the app having pushed the FULL tool schema (write/edit/bash
	// present) onto the shared client before the router runs.
	mock.SetTools(registry.GeminiTools())

	// Enable plan mode, then route a task.
	r.SetPlanMode(true)
	if _, _, err := r.Execute(context.Background(), nil, "analyze how the auth module works and where it's used"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// After Execute, the schema the client holds must contain read-only tools
	// only — no write/edit/bash/delete.
	names := declNames(mock.GetTools())
	for _, mutating := range []string{"write", "edit", "bash", "delete", "refactor"} {
		if names[mutating] {
			t.Errorf("plan mode: mutating tool %q must NOT be in the schema after Execute", mutating)
		}
	}
	// A representative read-only tool must survive.
	if !names["read"] && !names["grep"] {
		t.Error("plan mode: expected at least one read-only tool (read/grep) in the schema")
	}
}

// TestRouterExecute_NonPlanModeUnaffected guards against over-filtering: when
// plan mode is OFF, the router must not strip mutating tools that a task might
// need.
func TestRouterExecute_NonPlanModeUnaffected(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.EnqueueText("done")

	registry := tools.DefaultRegistry(t.TempDir())
	exec := tools.NewExecutor(registry, mock, time.Second)

	cfg := &RouterConfig{Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100}
	r := NewRouter(cfg, exec, nil, mock, registry, false, t.TempDir())
	mock.SetTools(registry.GeminiTools())

	// Plan mode OFF (default).
	if _, _, err := r.Execute(context.Background(), nil, "implement the feature"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	names := declNames(mock.GetTools())
	if !names["write"] && !names["edit"] {
		t.Error("non-plan mode: mutating tools (write/edit) must remain available")
	}
}
