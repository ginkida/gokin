package app

import (
	"context"
	"math"
	"sync"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type scopeUsageClient struct{ *testkit.MockClient }

func (c *scopeUsageClient) WithModel(string) client.Client { return c }

func activeUsageScopeApp() (*App, agent.InvocationScope) {
	app := &App{
		headlessRunActive: true,
		headlessTerminal:  &headlessTerminalOutcome{},
	}
	ctx := app.withHeadlessInvocationScope(context.Background())
	return app, agent.InvocationScopeFromContext(ctx)
}

func TestScopedAgentUsageExcludesOldCompletionButKeepsSessionTotals(t *testing.T) {
	app, current := activeUsageScopeApp()
	if current.IsZero() {
		t.Fatal("active headless scope is zero")
	}
	// Idempotent inside one lifecycle owner.
	if again := agent.InvocationScopeFromContext(app.withHeadlessInvocationScope(context.Background())); again != current {
		t.Fatalf("same invocation minted another scope: %v != %v", again, current)
	}

	old := agent.NewInvocationScope()
	app.accumulateScopedAgentResultUsage(&agent.AgentResult{
		InvocationScope:      old,
		InputTokens:          100,
		OutputTokens:         10,
		CacheReadInputTokens: 20,
		EstimatedCost:        0.40,
		CostTracked:          true,
	})
	usage, cost := app.headlessInvocationMetricsSnapshot()
	if usage.TotalTokens != 0 || cost.Tracked {
		t.Fatalf("old completion leaked into invocation: usage=%+v cost=%+v", usage, cost)
	}
	stats := app.GetTokenStats()
	if stats.InputTokens != 100 || stats.OutputTokens != 10 || stats.CacheReadInputTokens != 20 ||
		!stats.CostTracked || math.Abs(stats.EstimatedCost-0.40) > 1e-12 {
		t.Fatalf("old completion missing from session totals: %+v", stats)
	}

	app.accumulateScopedAgentResultUsage(&agent.AgentResult{
		InvocationScope:      current,
		InputTokens:          50,
		OutputTokens:         5,
		CacheReadInputTokens: 10,
		EstimatedCost:        0.20,
		CostTracked:          true,
	})
	usage, cost = app.headlessInvocationMetricsSnapshot()
	if usage.InputTokens != 50 || usage.OutputTokens != 5 || usage.CacheReadInputTokens != 10 || usage.TotalTokens != 55 {
		t.Fatalf("current foreground agent not attributed: %+v", usage)
	}
	if !cost.Tracked || math.Abs(cost.EstimatedUSD-0.20) > 1e-12 {
		t.Fatalf("current foreground cost not attributed: %+v", cost)
	}
	stats = app.GetTokenStats()
	if stats.InputTokens != 150 || stats.OutputTokens != 15 || stats.CacheReadInputTokens != 30 ||
		math.Abs(stats.EstimatedCost-0.60) > 1e-12 {
		t.Fatalf("session totals changed semantics: %+v", stats)
	}
}

func TestScopedAgentUsageConcurrentOldAndCurrentCompletions(t *testing.T) {
	app, current := activeUsageScopeApp()
	old := agent.NewInvocationScope()
	const perScope = 100

	var wg sync.WaitGroup
	for i := 0; i < perScope; i++ {
		for _, scope := range []agent.InvocationScope{old, current} {
			wg.Add(1)
			go func(scope agent.InvocationScope) {
				defer wg.Done()
				app.accumulateScopedAgentResultUsage(&agent.AgentResult{
					InvocationScope: scope,
					InputTokens:     3,
					OutputTokens:    2,
					EstimatedCost:   0.01,
					CostTracked:     true,
				})
			}(scope)
		}
	}
	wg.Wait()

	stats := app.GetTokenStats()
	if stats.InputTokens != perScope*2*3 || stats.OutputTokens != perScope*2*2 {
		t.Fatalf("session totals=%+v", stats)
	}
	usage, cost := app.headlessInvocationMetricsSnapshot()
	if usage.InputTokens != perScope*3 || usage.OutputTokens != perScope*2 || usage.TotalTokens != perScope*5 {
		t.Fatalf("invocation usage=%+v", usage)
	}
	if !cost.Tracked || math.Abs(cost.EstimatedUSD-float64(perScope)*0.01) > 1e-9 {
		t.Fatalf("invocation cost=%+v", cost)
	}
}

func TestScopedAgentUsageDoesNotAdvertisePartialCostAsTracked(t *testing.T) {
	app, current := activeUsageScopeApp()
	app.accumulateScopedAgentResultUsage(&agent.AgentResult{
		InvocationScope: current,
		InputTokens:     10,
		OutputTokens:    2,
		// No model/provider identity: pricing is genuinely unavailable.
	})
	app.accumulateScopedAgentResultUsage(&agent.AgentResult{
		InvocationScope: current,
		InputTokens:     5,
		OutputTokens:    1,
		EstimatedCost:   0.02,
		CostTracked:     true,
	})

	usage, cost := app.headlessInvocationMetricsSnapshot()
	if usage.TotalTokens != 18 {
		t.Fatalf("invocation usage = %+v", usage)
	}
	if cost.Tracked || math.Abs(cost.EstimatedUSD-0.02) > 1e-12 {
		t.Fatalf("partial invocation cost must stay explicitly untracked: %+v", cost)
	}
}

func TestForegroundTaskAgentUsageFlowsFromInvocationContextEndToEnd(t *testing.T) {
	app := &App{
		headlessRunActive: true,
		headlessTerminal:  &headlessTerminalOutcome{},
	}
	runCtx := app.withHeadlessInvocationScope(context.Background())
	if agent.InvocationScopeFromContext(runCtx).IsZero() {
		t.Fatal("headless run context was not scoped")
	}

	mock := &scopeUsageClient{MockClient: testkit.NewMockClient().EnqueueText("completed")}
	runner := agent.NewRunner(context.Background(), mock, tools.NewRegistry(), t.TempDir())
	runner.SetOnAgentUsage(func(_ string, result *agent.AgentResult) {
		app.accumulateScopedAgentResultUsage(result)
	})
	if _, err := runner.Spawn(runCtx, "general", "complete the task", 2, ""); err != nil {
		t.Fatalf("foreground task Spawn: %v", err)
	}

	usage, _ := app.headlessInvocationMetricsSnapshot()
	if usage.InputTokens == 0 || usage.TotalTokens == 0 {
		t.Fatalf("foreground task usage was not attributed end-to-end: %+v", usage)
	}
	stats := app.GetTokenStats()
	if stats.InputTokens != usage.InputTokens || stats.OutputTokens != usage.OutputTokens {
		t.Fatalf("session/invocation usage diverged for sole task: stats=%+v invocation=%+v", stats, usage)
	}
}

func TestHeadlessUsageScopeChangesWithLifecycleOwner(t *testing.T) {
	app, first := activeUsageScopeApp()
	app.mu.Lock()
	app.headlessTerminal = &headlessTerminalOutcome{}
	app.headlessInvocationUsage = HeadlessUsage{}
	app.headlessInvocationCost = HeadlessCost{}
	app.mu.Unlock()
	// There is a small lifecycle window before headless.go attaches the new
	// context scope. The previous scope must already be invalid because its
	// owner pointer no longer matches the fresh terminal token.
	app.accumulateScopedAgentResultUsage(&agent.AgentResult{
		InvocationScope: first, InputTokens: 7, OutputTokens: 3,
		EstimatedCost: 0.1, CostTracked: true,
	})
	usage, cost := app.headlessInvocationMetricsSnapshot()
	if usage.TotalTokens != 0 || cost.Tracked {
		t.Fatalf("previous lifecycle leaked before new scope attach: usage=%+v cost=%+v", usage, cost)
	}

	second := agent.InvocationScopeFromContext(app.withHeadlessInvocationScope(context.Background()))
	if second.IsZero() || second == first {
		t.Fatalf("next lifecycle reused scope: first=%v second=%v", first, second)
	}

	app.accumulateScopedAgentResultUsage(&agent.AgentResult{
		InvocationScope: first, InputTokens: 7, OutputTokens: 3,
		EstimatedCost: 0.1, CostTracked: true,
	})
	usage, cost = app.headlessInvocationMetricsSnapshot()
	if usage.TotalTokens != 0 || cost.Tracked {
		t.Fatalf("previous lifecycle leaked into next: usage=%+v cost=%+v", usage, cost)
	}
}
