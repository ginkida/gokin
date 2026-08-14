package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestToolCapabilityCeilingEmptyRemainsRestricted(t *testing.T) {
	ctx := ContextWithToolCapabilityCeiling(context.Background(), []string{})
	ceiling, restricted := ToolCapabilityCeilingFromContext(ctx)
	if !restricted || ceiling == nil || len(ceiling) != 0 {
		t.Fatalf("ceiling=%v restricted=%v, want explicit empty restriction", ceiling, restricted)
	}
}

func TestToolSchemaCeilingIsIndependentFromExecutionCapability(t *testing.T) {
	executor := &Executor{}
	ctx := ContextWithToolCapabilityCeiling(context.Background(), []string{"repl_exec", "harness"})
	ctx = ContextWithToolSchemaCeiling(ctx, executor, []string{"repl_exec"})

	capability, capabilityRestricted := ToolCapabilityCeilingFromContext(ctx)
	schema, schemaRestricted := ToolSchemaCeilingFromContext(ctx, executor)
	if !capabilityRestricted || !schemaRestricted ||
		!reflect.DeepEqual(capability, []string{"harness", "repl_exec"}) ||
		!reflect.DeepEqual(schema, []string{"repl_exec"}) {
		t.Fatalf("capability=%v/%t schema=%v/%t", capability, capabilityRestricted, schema, schemaRestricted)
	}
}

func TestExecutorEnforcesOwnModelSchemaButInternalInvocationRetainsAuthority(t *testing.T) {
	registry := NewRegistry()
	hidden := &scriptedStaticTool{name: "hidden", content: "proof"}
	registry.MustRegister(hidden)
	executor := NewExecutor(registry, nil, time.Second)
	ctx := ContextWithToolSchemaCeiling(t.Context(), executor, []string{})

	modelResult := executor.doExecuteTool(ctx, &genai.FunctionCall{
		ID: "model-call", Name: "hidden", Args: map[string]any{},
	})
	if modelResult.Success || modelResult.PolicyBlock == nil || hidden.calls != 0 {
		t.Fatalf("hidden model call = %+v calls=%d", modelResult, hidden.calls)
	}
	internalResult, err := executor.InvokeTool(ctx, "hidden", nil)
	if err != nil || !internalResult.Success || hidden.calls != 1 {
		t.Fatalf("trusted internal call = %+v err=%v calls=%d", internalResult, err, hidden.calls)
	}

	other := NewExecutor(registry, nil, time.Second)
	otherResult := other.doExecuteTool(ctx, &genai.FunctionCall{
		ID: "child-model-call", Name: "hidden", Args: map[string]any{},
	})
	if !otherResult.Success || hidden.calls != 2 {
		t.Fatalf("schema ceiling leaked to a different executor: %+v calls=%d", otherResult, hidden.calls)
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
