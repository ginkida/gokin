package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// statefulThenPanicClient lets the first provider round request a stateful
// tool, then panics while accepting that tool's function response. Returning
// itself from WithModel is intentional: Runner gives every Agent a model-scoped
// client, and the script/call counter must remain attached to that client.
type statefulThenPanicClient struct {
	*testkit.MockClient
	sends atomic.Int32
}

func newStatefulThenPanicClient() *statefulThenPanicClient {
	mock := testkit.NewMockClient().
		EnqueueToolCall("bash", map[string]any{"command": "stateful-once"}).
		EnqueueText("must not be returned")
	c := &statefulThenPanicClient{MockClient: mock}
	mock.OnSend = func(context.Context) {
		if c.sends.Add(1) == 2 {
			panic("provider parser exploded after tool execution")
		}
	}
	return c
}

func (c *statefulThenPanicClient) WithModel(string) client.Client { return c }

func newStatefulPanicRunner(t *testing.T) *Runner {
	t.Helper()
	registry := tools.NewRegistry()
	if err := registry.Register(&agentHookProbeTool{
		name: "bash", result: tools.NewSuccessResult("executed once"),
	}); err != nil {
		t.Fatalf("register stateful probe: %v", err)
	}
	return NewRunner(context.Background(), newStatefulThenPanicClient(), registry, t.TempDir())
}

func assertStoredStatefulPanicResult(t *testing.T, runner *Runner, agentID string, result *AgentResult, runErr error) {
	t.Helper()
	if agentID == "" {
		t.Fatal("panic after Agent.Run started lost the agent ID")
	}
	if runErr == nil || !strings.Contains(runErr.Error(), "panic") {
		t.Fatalf("run error = %v, want contained panic", runErr)
	}
	if result == nil {
		t.Fatal("panic did not synthesize an AgentResult")
	}
	if result.Status != AgentStatusFailed || !result.Completed {
		t.Fatalf("panic result status/completed = %s/%v, want failed/true", result.Status, result.Completed)
	}
	if result.StatefulToolAttempts != 1 {
		t.Fatalf("StatefulToolAttempts = %d, want 1", result.StatefulToolAttempts)
	}
	if !strings.Contains(result.Error, "provider parser exploded") {
		t.Fatalf("panic cause missing from result error: %q", result.Error)
	}

	stored, ok := runner.GetResult(agentID)
	if !ok {
		t.Fatal("panic result was not published before synchronous return")
	}
	if stored.Status != AgentStatusFailed || !stored.Completed || stored.StatefulToolAttempts != 1 {
		t.Fatalf("stored panic result = %+v", stored)
	}
	agent, ok := runner.GetAgent(agentID)
	if !ok || agent.GetStatus() != AgentStatusFailed {
		t.Fatalf("stored agent status = %v/%v, want failed", ok, agent)
	}
}

func TestSpawnContainsPanicAfterStatefulToolAndPublishesProvenance(t *testing.T) {
	runner := newStatefulPanicRunner(t)
	agentID, err := runner.Spawn(context.Background(), "general", "run one command", 3, "")
	result, _ := runner.GetResult(agentID)
	assertStoredStatefulPanicResult(t, runner, agentID, result, err)
}

func TestSpawnWithContextContainsPanicAfterStatefulToolAndPublishesProvenance(t *testing.T) {
	runner := newStatefulPanicRunner(t)
	agentID, result, err := runner.SpawnWithContext(
		context.Background(), "general", "run one command", 3, "", "", nil, false, nil)
	assertStoredStatefulPanicResult(t, runner, agentID, result, err)
}
