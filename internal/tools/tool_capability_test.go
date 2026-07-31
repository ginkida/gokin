package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestToolCapabilityCeilingEmptyRemainsRestricted(t *testing.T) {
	ctx := ContextWithToolCapabilityCeiling(context.Background(), []string{})
	ceiling, restricted := ToolCapabilityCeilingFromContext(ctx)
	if !restricted || ceiling == nil || len(ceiling) != 0 {
		t.Fatalf("ceiling=%v restricted=%v, want explicit empty restriction", ceiling, restricted)
	}
}

func TestToolsListHonorsInvocationCapabilityCeiling(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&scriptedStaticTool{name: "read"})
	registry.MustRegister(&scriptedStaticTool{name: "write"})
	list := NewToolsListTool(registry)
	registry.MustRegister(list)

	ctx := ContextWithToolCapabilityCeiling(context.Background(), []string{"read", "tools_list"})
	result, err := list.Execute(ctx, nil)
	if err != nil || !result.Success {
		t.Fatalf("tools_list result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Content, "**read**") ||
		strings.Contains(result.Content, "**write**") {
		t.Fatalf("tools_list escaped ceiling:\n%s", result.Content)
	}
}

type capabilityCapturingRunner struct {
	stubAgentRunner
	ceiling    []string
	restricted bool
}

func (r *capabilityCapturingRunner) Spawn(
	ctx context.Context,
	_, _ string,
	_ int,
	_ string,
) (string, error) {
	r.ceiling, r.restricted = ToolCapabilityCeilingFromContext(ctx)
	return "child-1", nil
}

func (r *capabilityCapturingRunner) GetResult(id string) (AgentResult, bool) {
	return AgentResult{
		AgentID:   id,
		Type:      "explore",
		Status:    "completed",
		Output:    "child output",
		Completed: true,
	}, true
}

func TestTaskToolIntersectsParentAndLocalCapabilities(t *testing.T) {
	runner := &capabilityCapturingRunner{}
	task := NewTaskTool()
	task.SetRunner(runner)
	task.SetToolCapabilityCeiling([]string{"read", "grep"})

	ctx := ContextWithToolCapabilityCeiling(context.Background(), []string{"read", "bash", "task"})
	result, err := task.Execute(ctx, map[string]any{
		"prompt":        "inspect",
		"subagent_type": "explore",
	})
	if err != nil || !result.Success {
		t.Fatalf("task result=%+v err=%v", result, err)
	}
	if !runner.restricted || !reflect.DeepEqual(runner.ceiling, []string{"read"}) {
		t.Fatalf("child ceiling=%v restricted=%v, want intersection [read]",
			runner.ceiling, runner.restricted)
	}
}
