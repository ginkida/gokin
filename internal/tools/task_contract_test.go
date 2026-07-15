package tools

import (
	"context"
	"strings"
	"testing"
)

type staticAgentTypeProvider []AgentTypeDefinition

func (p staticAgentTypeProvider) SnapshotAgentTypes() []AgentTypeDefinition {
	return append([]AgentTypeDefinition(nil), p...)
}

func TestTaskToolBackgroundPolicyIsDeclaredValidatedAndCloned(t *testing.T) {
	task := NewTaskTool()
	task.SetBackgroundAllowed(false)

	if _, ok := task.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("headless task declaration still exposes run_in_background")
	}
	args := map[string]any{
		"prompt":            "inspect code",
		"subagent_type":     "explore",
		"run_in_background": true,
	}
	if err := task.Validate(args); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("Validate error = %v, want background policy rejection", err)
	}
	result, err := task.Execute(context.Background(), args)
	if err != nil || result.Success || !strings.Contains(result.Error, "not available") {
		t.Fatalf("Execute result=%+v err=%v", result, err)
	}

	clone, ok := CloneToolForWorkDir(task, "").(*TaskTool)
	if !ok {
		t.Fatalf("clone type = %T", clone)
	}
	if _, ok := clone.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("task clone lost background policy")
	}
}

func TestEmptyAgentToolCapabilityCeilingRemainsRestricted(t *testing.T) {
	ctx := ContextWithAgentToolCapabilityCeiling(context.Background(), []string{})
	ceiling, restricted := AgentToolCapabilityCeilingFromContext(ctx)
	if !restricted || ceiling == nil || len(ceiling) != 0 {
		t.Fatalf("ceiling=%v restricted=%v, want non-nil empty restriction", ceiling, restricted)
	}

	base := NewRegistry()
	base.MustRegister(NewTaskTool())
	cloned := CloneRegistryForWorkDirWithToolCeiling(base, "", ceiling)
	if names := cloned.Names(); len(names) != 0 {
		t.Fatalf("empty ceiling cloned tools %v", names)
	}
}

func TestLazyRegistryUsesLiveTaskDeclaration(t *testing.T) {
	registry := DefaultLazyRegistry(t.TempDir())
	tool, ok := registry.Get("task")
	if !ok {
		t.Fatal("lazy task tool missing")
	}
	task := tool.(*TaskTool)
	task.SetAgentTypeProvider(staticAgentTypeProvider{{
		Name: "reviewer", Description: "Reviews changes",
	}})

	var taskDeclarationFound bool
	for _, declaration := range registry.Declarations() {
		if declaration.Name != "task" {
			continue
		}
		taskDeclarationFound = true
		if !containsTaskContractString(
			declaration.Parameters.Properties["subagent_type"].Enum, "reviewer") {
			t.Fatalf("lazy task enum = %v", declaration.Parameters.Properties["subagent_type"].Enum)
		}
	}
	if !taskDeclarationFound {
		t.Fatal("lazy task declaration missing")
	}
}

func containsTaskContractString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
