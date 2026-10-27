package agent

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestExecutionAccountingIgnoresNonterminalAndCancellation(t *testing.T) {
	optimizer := NewStrategyOptimizer(t.TempDir())
	deps := runnerAgentDeps{strategyOptimizer: optimizer}
	runner := &Runner{}
	for _, status := range []AgentStatus{AgentStatusRunning, AgentStatusRunning, AgentStatusCancelled, AgentStatusCompleted, AgentStatusFailed} {
		runner.recordAgentExecutionLearning(deps, "general", "inspect", &AgentResult{Status: status}, time.Second, "spawn")
	}
	metrics, ok := optimizer.GetMetrics("general")
	if !ok || metrics.SuccessCount != 1 || metrics.FailureCount != 1 {
		t.Fatalf("execution counts = %+v; want one completed success and one failure", metrics)
	}
}

func TestExecutionAccountingMigrationAndOrderedPersistence(t *testing.T) {
	root := t.TempDir()
	optimizer := NewStrategyOptimizer(root)
	legacy := []byte(`{"general":{"strategy_name":"general","success_count":80,"failure_count":13196,"task_types":{"spawn":13276}}}`)
	if err := os.WriteFile(optimizer.storagePath(), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	optimizer = NewStrategyOptimizer(root)
	metric, _ := optimizer.GetMetrics("general")
	if metric.SuccessCount != 0 || metric.FailureCount != 0 || metric.AccountingVersion != 2 {
		t.Fatalf("legacy counts retained: %+v", metric)
	}
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Add(1)
		go func() { defer workers.Done(); optimizer.RecordExecution("general", "spawn", i%2 == 0, time.Second) }()
	}
	workers.Wait()
	reloaded := NewStrategyOptimizer(root)
	metric, _ = reloaded.GetMetrics("general")
	if metric.SuccessCount != 10 || metric.FailureCount != 10 || metric.TaskTypes["spawn"] != 20 {
		t.Fatalf("persisted counts=%+v", metric)
	}
	if err := optimizer.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(NewStrategyOptimizer(root).GetAllMetrics()) != 0 {
		t.Fatal("cleared metrics resurrected")
	}
}
