package agent

import (
	"context"
	"path/filepath"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type agentDontAskTool struct {
	name  string
	calls int
}

func (t *agentDontAskTool) Name() string {
	if t.name != "" {
		return t.name
	}
	return "permission_probe"
}
func (t *agentDontAskTool) Description() string { return "records execution" }
func (t *agentDontAskTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.Description()}
}

func TestAgentBindsScopedPermissionRulesToIsolatedWorkDir(t *testing.T) {
	workDir := t.TempDir()
	probe := &agentDontAskTool{name: "write"}
	registry := tools.NewRegistry()
	registry.MustRegister(probe)
	manager := permission.NewManager(permission.DefaultRules(), true)
	manager.SetDontAsk(true)
	manager.SetRunToolRules([]string{"edit(/allowed/**)"}, nil)
	agent := NewAgent(
		AgentTypeGeneral, nil, registry, workDir, 2, "", manager, nil,
	)

	allowed := agent.executeTool(context.Background(), &genai.FunctionCall{
		Name: "write",
		Args: map[string]any{
			"file_path": filepath.Join(workDir, "allowed", "agent.go"),
		},
	})
	if !allowed.Success || probe.calls != 1 {
		t.Fatalf("in-worktree scoped write=%+v calls=%d", allowed, probe.calls)
	}

	blocked := agent.executeTool(context.Background(), &genai.FunctionCall{
		Name: "write",
		Args: map[string]any{
			"file_path": filepath.Join(t.TempDir(), "outside.go"),
		},
	})
	if blocked.Success || blocked.PolicyBlock == nil || probe.calls != 1 {
		t.Fatalf("outside-worktree scoped write=%+v calls=%d", blocked, probe.calls)
	}
}
func (t *agentDontAskTool) Validate(map[string]any) error { return nil }
func (t *agentDontAskTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.calls++
	return tools.NewSuccessResult("executed"), nil
}

func TestAgentInheritsDontAskAndRunPreapprovals(t *testing.T) {
	probe := &agentDontAskTool{}
	registry := tools.NewRegistry()
	registry.MustRegister(probe)
	manager := permission.NewManager(permission.DefaultRules(), true)
	manager.SetDontAsk(true)
	agent := NewAgent(
		AgentTypeGeneral, nil, registry, t.TempDir(), 2, "", manager, nil,
	)
	call := &genai.FunctionCall{Name: probe.Name(), Args: map[string]any{}}

	blocked := agent.executeTool(context.Background(), call)
	if blocked.Success || blocked.PolicyBlock == nil ||
		blocked.PolicyBlock.Kind != tools.PolicyBlockPermission || probe.calls != 0 {
		t.Fatalf("subagent dontAsk result=%+v calls=%d", blocked, probe.calls)
	}

	manager.SetRunToolRules([]string{probe.Name()}, nil)
	allowed := agent.executeTool(context.Background(), call)
	if !allowed.Success || probe.calls != 1 {
		t.Fatalf("subagent preapproval result=%+v calls=%d", allowed, probe.calls)
	}
}
