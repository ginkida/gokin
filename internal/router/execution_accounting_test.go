package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestSmartRouterCancellationIsNotFailedExecution(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueError(context.Canceled)
	executor := tools.NewExecutor(tools.NewRegistry(), mock, time.Second)
	router := NewSmartRouter(t.Context(), nil, executor, nil, mock, t.TempDir())
	router.Router.registry = tools.NewRegistry()
	optimizer := agent.NewStrategyOptimizer(t.TempDir())
	router.SetStrategyOptimizer(optimizer)
	if _, _, err := router.Execute(t.Context(), nil, "What is 2+2?"); err == nil {
		t.Fatal("expected cancelled execution")
	}
	if len(optimizer.GetAllMetrics()) != 0 {
		t.Fatal("cancellation entered execution failure denominator")
	}
}

func TestSmartRouterDelegatedEnvelopeIsNotAnExecution(t *testing.T) {
	mock := testkit.NewMockClient()
	runner := &stubRunner{spawnID: "agent-one", resultOK: true, result: &agent.AgentResult{Status: agent.AgentStatusCompleted, Output: "Completed inspection."}}
	executor := tools.NewExecutor(tools.NewRegistry(), mock, time.Second)
	router := NewSmartRouter(t.Context(), nil, executor, runner, mock, t.TempDir())
	router.Router.registry = tools.NewRegistry()
	optimizer := agent.NewStrategyOptimizer(t.TempDir())
	router.SetStrategyOptimizer(optimizer)
	const prompt = "Run the inspection in background"
	decision := router.Route(prompt)
	if decision.Handler != HandlerSubAgent {
		t.Fatalf("fixture routed to %s", decision.Handler)
	}
	// Runner owns the eventual terminal result; the router only acknowledges
	// dispatch here, before any background execution has completed.
	if _, _, err := router.Execute(t.Context(), nil, prompt); err != nil {
		t.Fatal(err)
	}
	if len(optimizer.GetAllMetrics()) != 0 {
		t.Fatal("dispatch acknowledgement counted as successful execution")
	}
}

func TestRoutedDegenerateOutputPreservesPartialWorkAndFailure(t *testing.T) {
	failure := &client.DegenerateOutputError{Model: "glm-5", Tool: "read", Bytes: 32768, Period: 37, Exhausted: true}
	runner := &stubRunner{spawnID: "failed-agent", spawnErr: failure, resultOK: true, result: &agent.AgentResult{Status: agent.AgentStatusFailed, Output: "Already inspected the source.", Error: failure.Error()}}
	router := &Router{agentRunner: runner}
	history, output, err := router.executeViaSubAgent(t.Context(), "inspect", "general", false)
	if _, ok := client.OutputRecoveryError(err); !ok || !strings.Contains(output, "Already inspected") || len(history) == 0 {
		t.Fatalf("failed read-only agent was turned into success or lost its work: %q %v", output, err)
	}
}
