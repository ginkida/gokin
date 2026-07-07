package app

import (
	"context"
	"os"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/tools"
)

// Compile-time proof the adapter actually satisfies both interfaces —
// task_stop.go/task_output.go's type assertions (`t.runner.(AgentCanceller)`/
// `.(AgentLister)`) against this exact type must succeed.
var (
	_ tools.AgentCanceller = (*agentRunnerAdapter)(nil)
	_ tools.AgentLister    = (*agentRunnerAdapter)(nil)
)

// TestAgentRunnerAdapter_CancelAndListAgentsForward (round 5) pins the fix:
// agentRunnerAdapter — the ONE runner instance wired into task/task_output/
// task_stop (builder.go: `runnerAdapter := &agentRunnerAdapter{...}`, shared
// by all three via SetRunner) — implemented neither tools.AgentCanceller nor
// tools.AgentLister, even though the underlying *agent.Runner fully supports
// both. task_stop.go's stopAgent and task_output.go's cancelTask/listTasks
// type-assert the runner against these interfaces and silently no-op
// ("agent cancellation not supported" / an always-empty "Agent Tasks:"
// section) when the assertion fails — which it always did. Fixed by adding
// forwarding Cancel/ListAgents methods.
func TestAgentRunnerAdapter_CancelAndListAgentsForward(t *testing.T) {
	// A plain os.MkdirTemp (not t.TempDir()) — SpawnAsync's trailing
	// bookkeeping (agent-output file finalization) can still be touching
	// this directory for a moment AFTER WaitWithTimeout observes Completed
	// (notifyResultReady fires before that trailing work finishes), and
	// t.TempDir()'s cleanup FAILS the test outright on "directory not empty"
	// rather than just leaking — best-effort RemoveAll instead.
	workDir, err := os.MkdirTemp("", "agent-runner-adapter-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(workDir)

	registry := tools.NewRegistry()
	runner := agent.NewRunner(context.Background(), nil, registry, workDir)
	adapter := &agentRunnerAdapter{runner: runner}

	// ListAgents forwards and reflects the runner's actual tracked agents.
	if got := adapter.ListAgents(); len(got) != 0 {
		t.Fatalf("ListAgents() on an empty runner = %v, want empty", got)
	}

	agentID := runner.SpawnAsync(context.Background(), "nonexistent-type", "test prompt", 3, "")
	if agentID == "" {
		t.Fatal("expected SpawnAsync to return a non-empty agent ID even for an unregistered type (it should fail async, not synchronously)")
	}

	found := false
	for _, id := range adapter.ListAgents() {
		if id == agentID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListAgents() = %v, want it to include the spawned agent %q", adapter.ListAgents(), agentID)
	}

	// Cancel forwards to the real runner and actually takes effect — a
	// subsequent GetResult must reflect the cancellation (Status/Completed),
	// exactly what task_stop.go relies on to report success.
	if err := adapter.Cancel(agentID); err != nil {
		t.Fatalf("Cancel(%q) = %v, want nil (the agent exists and should be cancellable)", agentID, err)
	}

	// Unknown agent ID must still surface the underlying runner's error, not
	// panic or silently succeed.
	if err := adapter.Cancel("does-not-exist"); err == nil {
		t.Fatal("Cancel on an unknown agent ID should return an error, not nil")
	}

	// Wait for the spawned agent's background goroutine to actually finish
	// before the test returns — otherwise it can still be touching t.TempDir()
	// (e.g. writing agent-output files) when TempDir's own cleanup runs,
	// flaking with "directory not empty" on unrelated test runs.
	_, _ = runner.WaitWithTimeout(agentID, 5*time.Second)
}
