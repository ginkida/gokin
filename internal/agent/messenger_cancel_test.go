package agent

import (
	"context"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// TestMessenger_ParentCtxDerivesFromOwningAgentRunContext pins the fix for
// the "ask_agent helper keeps running after the parent is cancelled" class:
// AgentMessenger.parentCtx() must return the OWNING agent's live run
// context (the one Agent.Cancel's cancelFunc actually kills), not the
// app-lifetime context captured once at Runner construction — otherwise
// cancelling the parent (Esc, task_stop) never reaches a helper agent
// spawned via ask_agent/delegate.
func TestMessenger_ParentCtxDerivesFromOwningAgentRunContext(t *testing.T) {
	runner := NewRunner(context.Background(), testkit.NewMockClient(), tools.NewRegistry(), t.TempDir())

	parent := NewAgent(AgentTypeGeneral, testkit.NewMockClient(), tools.NewRegistry(), t.TempDir(), 5, "", nil, nil)
	runner.mu.Lock()
	runner.agents[parent.ID] = parent
	runner.mu.Unlock()

	// Simulate what Spawn does: wrap a cancellable ctx, register it as the
	// agent's live run ctx (as Agent.Run's first line now does) AND as its
	// cancelFunc (as the Spawn fix now does).
	appCtx := context.Background()
	runCtx, runCancel := context.WithCancel(appCtx)
	parent.stateMu.Lock()
	parent.runCtx = runCtx
	parent.status = AgentStatusRunning
	parent.stateMu.Unlock()
	parent.SetCancelFunc(runCancel)

	messenger := NewAgentMessenger(appCtx, runner, parent.ID)

	got := messenger.parentCtx()
	if got != runCtx {
		t.Fatal("parentCtx() must return the OWNING agent's live run ctx, not the app-lifetime ctx captured at construction")
	}

	// Cancelling the parent (what Esc / task_stop does) must be visible
	// through parentCtx() — that's the entire point of deriving from it.
	parent.Cancel()
	select {
	case <-messenger.parentCtx().Done():
	default:
		t.Fatal("cancelling the parent agent must cancel the context parentCtx() returns — helper agents would keep running otherwise")
	}
}

// TestMessenger_ParentCtxFallsBackWhenAgentUnknown guards the fallback path:
// an unresolvable fromAgentID (test double, race at construction) must not
// panic — it falls back to the constructor's ctx.
func TestMessenger_ParentCtxFallsBackWhenAgentUnknown(t *testing.T) {
	runner := NewRunner(context.Background(), testkit.NewMockClient(), tools.NewRegistry(), t.TempDir())
	appCtx := context.Background()
	messenger := NewAgentMessenger(appCtx, runner, "does-not-exist")
	if messenger.parentCtx() != appCtx {
		t.Fatal("parentCtx() must fall back to the constructor ctx when the agent can't be resolved")
	}
}
