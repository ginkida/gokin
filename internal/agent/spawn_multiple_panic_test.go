package agent

import (
	"context"
	"os"
	"testing"

	"gokin/internal/tools"
)

// A panicking SpawnMultiple worker used to fail twice over: it LEAKED the
// isolated git worktree (only the normal flow finalized; every other spawn
// path finalizes from its recovery defer too), and it swallowed the panic at
// the API boundary — the call returned an EMPTY agent id with a NIL error, so
// the caller could not tell "never started" from "crashed after doing work"
// and GetResult had nothing to look up.
//
// A nil client makes agent.Run nil-deref inside the worker: the realistic
// shape of an unexpected panic occurring AFTER the workspace exists.
func TestSpawnMultiplePanicSurfacesResultAndCleansWorkspace(t *testing.T) {
	workDir := t.TempDir()
	registry := tools.NewRegistry()
	registry.MustRegister(tools.NewReadTool(workDir))

	runner := NewRunner(context.Background(), nil, registry, workDir)
	runner.SetWorkspaceIsolationEnabled(true)

	typeRegistry := NewAgentTypeRegistry()
	if err := typeRegistry.RegisterDynamic("reviewer", "read-only reviewer", []string{"read"}, "prompt"); err != nil {
		t.Fatalf("RegisterDynamic: %v", err)
	}
	runner.SetTypeRegistry(typeRegistry)

	ids, err := runner.SpawnMultiple(context.Background(), []AgentTask{
		{Type: "reviewer", Prompt: "review", MaxTurns: 1},
	})

	if err == nil {
		t.Fatal("a panicked worker must surface an error to the caller")
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("panicked worker must still report its agent id, got %#v", ids)
	}

	result, ok := runner.GetResult(ids[0])
	if !ok || result == nil {
		t.Fatal("the synthesized panic result must be retrievable via GetResult")
	}
	if result.Status != AgentStatusFailed {
		t.Fatalf("panic result status = %v, want failed", result.Status)
	}

	runner.mu.RLock()
	agent := runner.agents[ids[0]]
	runner.mu.RUnlock()
	if agent == nil || agent.isolatedWorkspace == nil {
		t.Fatal("precondition: the agent must have had an isolated workspace")
	}
	if _, statErr := os.Stat(agent.workDir); !os.IsNotExist(statErr) {
		t.Fatalf("panic path leaked the isolated worktree at %s (stat err=%v)", agent.workDir, statErr)
	}
}
