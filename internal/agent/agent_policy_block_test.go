package agent

import (
	"context"
	"testing"

	"google.golang.org/genai"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type agentPolicyResultTool struct{}

func (agentPolicyResultTool) Name() string        { return "policy_probe" }
func (agentPolicyResultTool) Description() string { return "returns a typed policy refusal" }
func (agentPolicyResultTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "policy_probe", Description: "typed policy probe"}
}
func (agentPolicyResultTool) Validate(map[string]any) error { return nil }
func (agentPolicyResultTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "probe denied"), nil
}

func TestAgentResultCarriesTurnScopedPolicyBlockAfterModelProse(t *testing.T) {
	registry := tools.NewRegistry()
	if err := registry.Register(agentPolicyResultTool{}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	mock := testkit.NewMockClient().
		EnqueueToolCall("policy_probe", map[string]any{}).
		EnqueueText("I could not run it, but this is a normal model response.").
		EnqueueText("A clean later run.")
	agent := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
	agent.client = mock

	blocked, err := agent.Run(context.Background(), "try protected work")
	if err != nil {
		t.Fatalf("Run() error = %v; model calls = %+v", err, mock.Calls())
	}
	if blocked.Status != AgentStatusCompleted {
		t.Fatalf("status = %s, want conversational completion", blocked.Status)
	}
	if blocked.PolicyBlock == nil || blocked.PolicyBlock.Kind != tools.PolicyBlockPermission {
		t.Fatalf("completed result lost policy refusal: %#v", blocked.PolicyBlock)
	}

	clean, err := agent.Run(context.Background(), "answer without a tool")
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if clean.PolicyBlock != nil {
		t.Fatalf("policy refusal leaked into later run: %#v", clean.PolicyBlock)
	}
}
