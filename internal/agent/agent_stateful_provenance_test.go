package agent

import (
	"context"
	"testing"

	"google.golang.org/genai"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestAgentStatefulToolAttemptsUseFailClosedExecutionBoundary(t *testing.T) {
	readProbe := &agentHookProbeTool{name: "read", result: tools.NewSuccessResult("read")}
	remoteProbe := &agentHookProbeTool{name: "custom_remote_mutation", result: tools.NewSuccessResult("changed")}
	registry := tools.NewRegistry()
	if err := registry.Register(readProbe); err != nil {
		t.Fatalf("register read probe: %v", err)
	}
	if err := registry.Register(remoteProbe); err != nil {
		t.Fatalf("register remote probe: %v", err)
	}
	agent := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 1, "", nil, nil)

	if result := agent.executeTool(context.Background(), &genai.FunctionCall{
		ID: "read-1", Name: "read", Args: map[string]any{},
	}); !result.Success {
		t.Fatalf("read result = %#v", result)
	}
	if got := agent.StatefulToolAttemptCount(); got != 0 {
		t.Fatalf("explicitly side-effect-free read counted as stateful: %d", got)
	}

	if result := agent.executeTool(context.Background(), &genai.FunctionCall{
		ID: "remote-1", Name: "custom_remote_mutation", Args: map[string]any{},
	}); !result.Success {
		t.Fatalf("remote result = %#v", result)
	}
	if got := agent.StatefulToolAttemptCount(); got != 1 {
		t.Fatalf("unknown tool attempts = %d, want 1 (fail closed)", got)
	}
}

func TestAgentResultStatefulToolAttemptsArePerRunAndIncludeBash(t *testing.T) {
	probe := &agentHookProbeTool{name: "bash", result: tools.NewSuccessResult("ran")}
	registry := tools.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatalf("register bash probe: %v", err)
	}
	mock := testkit.NewMockClient().
		EnqueueToolCall("bash", map[string]any{"command": "do-once"}).
		EnqueueText("first run complete").
		EnqueueText("second run is read-only")
	agent := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 3, "", nil, nil)
	agent.client = mock

	first, err := agent.Run(context.Background(), "run the command")
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if first.StatefulToolAttempts != 1 {
		t.Fatalf("first StatefulToolAttempts = %d, want 1", first.StatefulToolAttempts)
	}
	if first.MutatingToolCalls != 0 {
		t.Fatalf("bash unexpectedly entered narrow implementation count: %d", first.MutatingToolCalls)
	}

	second, err := agent.Run(context.Background(), "answer without tools")
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}
	if second.StatefulToolAttempts != 0 {
		t.Fatalf("stateful provenance leaked across Run invocations: %d", second.StatefulToolAttempts)
	}
}
