package agent

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/tools"
)

type taskSelectionRunner struct {
	mu          sync.Mutex
	spawnedType string
	spawnCalls  int
	ceiling     []string
	result      tools.AgentResult
}

func (r *taskSelectionRunner) Spawn(ctx context.Context, agentType, _ string, _ int, _ string) (string, error) {
	ceiling, _ := tools.AgentToolCapabilityCeilingFromContext(ctx)
	r.mu.Lock()
	r.spawnedType = agentType
	r.spawnCalls++
	r.ceiling = ceiling
	r.mu.Unlock()
	return r.result.AgentID, nil
}

func (r *taskSelectionRunner) SpawnAsync(ctx context.Context, agentType, prompt string, maxTurns int, model string) string {
	id, _ := r.Spawn(ctx, agentType, prompt, maxTurns, model)
	return id
}

func (r *taskSelectionRunner) SpawnAsyncWithStreaming(ctx context.Context, agentType, prompt string, maxTurns int, model string, _ func(string), _ func(string, *tools.AgentProgress)) string {
	return r.SpawnAsync(ctx, agentType, prompt, maxTurns, model)
}

func (r *taskSelectionRunner) Resume(context.Context, string, string) (string, error) {
	return r.result.AgentID, nil
}

func (r *taskSelectionRunner) ResumeAsync(context.Context, string, string) (string, error) {
	return r.result.AgentID, nil
}

func (r *taskSelectionRunner) GetResult(agentID string) (tools.AgentResult, bool) {
	if agentID != r.result.AgentID {
		return tools.AgentResult{}, false
	}
	return r.result, true
}

func (r *taskSelectionRunner) snapshot() (string, int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spawnedType, r.spawnCalls, append([]string(nil), r.ceiling...)
}

func TestFileAgentTypeIsDeclaredValidatedAndExecutedByTaskTool(t *testing.T) {
	agentsDir := t.TempDir()
	writeAgentFile(t, agentsDir, "security-auditor.md", `---
description: Audits changes for injection and secret exposure
tools: [read, grep]
---
Review the requested code and report security risks.`)

	typeRegistry := NewAgentTypeRegistry()
	loaded, warnings := LoadAgentTypeFiles(typeRegistry, agentsDir, "")
	if len(warnings) != 0 || len(loaded) != 1 {
		t.Fatalf("LoadAgentTypeFiles loaded=%v warnings=%v", loaded, warnings)
	}

	runner := &taskSelectionRunner{result: tools.AgentResult{
		AgentID:   "agent-security",
		Type:      "security-auditor",
		Status:    "completed",
		Output:    "No injection path found.",
		Duration:  time.Millisecond,
		Completed: true,
	}}
	task := tools.NewTaskTool()
	task.SetAgentTypeProvider(typeRegistry)
	task.SetRunner(runner)

	declaration := task.Declaration()
	subagentSchema := declaration.Parameters.Properties["subagent_type"]
	if !containsString(subagentSchema.Enum, "security-auditor") {
		t.Fatalf("subagent enum = %v, missing file-defined type", subagentSchema.Enum)
	}
	if !strings.Contains(declaration.Description, "security-auditor: Audits changes") {
		t.Fatalf("task declaration does not describe custom type:\n%s", declaration.Description)
	}

	args := map[string]any{
		"prompt":        "audit the parser",
		"subagent_type": "security-auditor",
	}
	if err := task.Validate(args); err != nil {
		t.Fatalf("Validate file-defined type: %v", err)
	}
	result, err := task.Execute(context.Background(), args)
	if err != nil || !result.Success {
		t.Fatalf("Execute result=%+v err=%v", result, err)
	}
	spawnedType, calls, _ := runner.snapshot()
	if spawnedType != "security-auditor" || calls != 1 {
		t.Fatalf("runner received type=%q calls=%d", spawnedType, calls)
	}

	unknown := map[string]any{"prompt": "x", "subagent_type": "missing-agent"}
	result, err = task.Execute(context.Background(), unknown)
	if err != nil || result.Success || !strings.Contains(result.Error, `unknown agent type "missing-agent"`) {
		t.Fatalf("unknown Execute result=%+v err=%v", result, err)
	}
	_, calls, _ = runner.snapshot()
	if calls != 1 {
		t.Fatalf("unknown type reached runner; spawn calls=%d", calls)
	}
}

func TestClonedTaskToolPreservesCatalogAndParentCapabilityCeiling(t *testing.T) {
	workDir := t.TempDir()
	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic(
		"writer", "Writes reviewed changes", []string{"read", "write"}, "write carefully"); err != nil {
		t.Fatal(err)
	}

	runner := &taskSelectionRunner{result: tools.AgentResult{
		AgentID: "writer-agent", Type: "writer", Status: "completed", Completed: true,
	}}
	task := tools.NewTaskTool()
	task.SetAgentTypeProvider(typeRegistry)
	task.SetRunner(runner)

	base := tools.NewRegistry()
	base.MustRegister(task)
	base.MustRegister(tools.NewReadTool(workDir))
	base.MustRegister(tools.NewWriteTool(workDir))

	// The parent deliberately lacks write. Its cloned task tool must keep the
	// live type catalog/runner while handing the child only [read, task].
	parent := NewAgentWithDynamicType(&DynamicAgentType{
		Name:         "reviewer",
		AllowedTools: []string{"read", "task"},
	}, nil, base, workDir, 2, "", nil, nil)
	clonedTool, ok := parent.registry.Get("task")
	if !ok {
		t.Fatal("parent task tool missing")
	}
	clonedTask, ok := clonedTool.(*tools.TaskTool)
	if !ok {
		t.Fatalf("parent task tool type = %T", clonedTool)
	}
	result, err := clonedTask.Execute(context.Background(), map[string]any{
		"prompt":        "make a change",
		"subagent_type": "writer",
	})
	if err != nil || !result.Success {
		t.Fatalf("cloned task Execute result=%+v err=%v", result, err)
	}
	_, _, ceiling := runner.snapshot()
	if want := []string{"read", "task"}; !reflect.DeepEqual(ceiling, want) {
		t.Fatalf("child capability ceiling=%v, want %v", ceiling, want)
	}
}

func TestRunnerIntersectsDynamicAllowlistWithParentCapabilityCeiling(t *testing.T) {
	workDir := t.TempDir()
	base := tools.NewRegistry()
	base.MustRegister(tools.NewReadTool(workDir))
	base.MustRegister(tools.NewWriteTool(workDir))

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic(
		"writer", "Writes files", []string{"read", "write"}, "write carefully"); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(context.Background(), nil, base, workDir)
	runner.SetTypeRegistry(typeRegistry)

	ctx := tools.ContextWithAgentToolCapabilityCeiling(context.Background(), []string{"read"})
	child := runner.newConfiguredAgent(ctx, runner.snapshotAgentDeps(), "writer", 2, "", nil)
	if _, ok := child.registry.Get("read"); !ok {
		t.Fatal("child lost the tool allowed by both parent and dynamic type")
	}
	if _, ok := child.registry.Get("write"); ok {
		t.Fatal("dynamic type expanded beyond parent capability ceiling")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
