package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type invocationScopeClient struct{ *testkit.MockClient }

func (c *invocationScopeClient) WithModel(string) client.Client { return c }

func TestInvocationScopeContextIsUniqueAndInherited(t *testing.T) {
	first := NewInvocationScope()
	second := NewInvocationScope()
	if first.IsZero() || second.IsZero() || first == second {
		t.Fatalf("invalid scopes: first=%v second=%v", first, second)
	}

	ctx := WithInvocationScope(context.Background(), first)
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := InvocationScopeFromContext(child); got != first {
		t.Fatalf("descendant scope=%v, want %v", got, first)
	}
	if got := InvocationScopeFromContext(context.Background()); !got.IsZero() {
		t.Fatalf("unscoped context returned %v", got)
	}
}

func TestRunnerCapturesScopeAndUsageCallbackRecoversItForSyntheticResult(t *testing.T) {
	registry := tools.NewRegistry()
	runner := NewRunner(context.Background(), nil, registry, t.TempDir())
	scope := NewInvocationScope()
	ctx := WithInvocationScope(context.Background(), scope)
	configured := runner.newConfiguredAgent(ctx, runner.snapshotAgentDeps(), "general", 1, "", nil)
	if configured.invocationScope != scope {
		t.Fatalf("configured scope=%v, want %v", configured.invocationScope, scope)
	}

	runner.mu.Lock()
	runner.agents[configured.ID] = configured
	runner.mu.Unlock()
	got := InvocationScope{}
	runner.SetOnAgentUsage(func(_ string, result *AgentResult) {
		got = result.InvocationScope
	})
	// Panic/nil-result recovery can construct a terminal result outside
	// Agent.Run. reportAgentUsage must still recover the spawn scope.
	runner.reportAgentUsage(configured.ID, &AgentResult{InputTokens: 10})
	if got != scope {
		t.Fatalf("callback scope=%v, want %v", got, scope)
	}
}

func TestResumeRebindsUsageToCurrentInvocationScope(t *testing.T) {
	mock := &invocationScopeClient{MockClient: testkit.NewMockClient()}
	mock.EnqueueText("first complete")
	mock.EnqueueText("resume complete")
	mock.EnqueueText("async resume complete")
	runner := NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetStore(store)

	var reported []InvocationScope
	runner.SetOnAgentUsage(func(_ string, result *AgentResult) {
		reported = append(reported, result.InvocationScope)
	})
	first := NewInvocationScope()
	agentID, err := runner.Spawn(
		WithInvocationScope(context.Background(), first), "general", "first task", 2, "")
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	second := NewInvocationScope()
	resumedID, err := runner.Resume(
		WithInvocationScope(context.Background(), second), agentID, "continue task")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumedID != agentID {
		t.Fatalf("resumed ID=%q, want %q", resumedID, agentID)
	}
	third := NewInvocationScope()
	asyncID, err := runner.ResumeAsync(
		WithInvocationScope(context.Background(), third), agentID, "continue asynchronously")
	if err != nil {
		t.Fatalf("ResumeAsync: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := runner.WaitWithContext(waitCtx, asyncID); err != nil {
		t.Fatalf("WaitWithContext: %v", err)
	}
	if len(reported) != 3 || reported[0] != first || reported[1] != second || reported[2] != third {
		t.Fatalf("reported scopes=%v, want [%v %v %v]", reported, first, second, third)
	}
}
