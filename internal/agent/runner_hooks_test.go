package agent

import (
	"context"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/tools"
)

func TestRunnerSetHooksUpdatesExistingAndFutureAgents(t *testing.T) {
	registry := tools.NewRegistry()
	runner := NewRunner(context.Background(), nil, registry, t.TempDir())
	existing := NewAgent(AgentTypeGeneral, nil, registry, runner.workDir, 1, "", nil, nil)
	runner.mu.Lock()
	runner.agents[existing.ID] = existing
	runner.mu.Unlock()

	manager := hooks.NewManager(true, runner.workDir)
	runner.SetHooks(manager)

	existing.stateMu.RLock()
	existingHooks := existing.hooks
	existing.stateMu.RUnlock()
	if existingHooks != manager {
		t.Fatal("SetHooks did not update an already-created agent")
	}

	deps := runner.snapshotAgentDeps()
	future := runner.newConfiguredAgent(context.Background(), deps, string(AgentTypeGeneral), 1, "", nil)
	future.stateMu.RLock()
	futureHooks := future.hooks
	future.stateMu.RUnlock()
	if futureHooks != manager {
		t.Fatal("newly configured agent did not inherit Runner hooks")
	}
}
