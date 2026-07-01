package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/tools"
)

// TestSpawnResponse_PreservesPartialOutputOnSpawnError pins the fix for the
// exact bug shape already fixed in router.go's executeViaSubAgent but missed
// in messenger.go: Spawn is synchronous and stores the agent's PARTIAL
// result (Output + Error) into runner.results BEFORE returning a non-nil
// err, so a delegate/helper agent that did real work before failing (round
// timeout, max-turn-limit, etc.) must still surface that work, not just the
// bare spawn error.
func TestSpawnResponse_PreservesPartialOutputOnSpawnError(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	runner.mu.Lock()
	runner.results["a1"] = &AgentResult{
		AgentID: "a1",
		Output:  "Implemented restore():\n+func Restore() error { ... }",
		Error:   "model round timeout",
	}
	runner.mu.Unlock()

	resp := spawnResponse(runner, "a1", errors.New("model round timeout"),
		"Delegation failed", "Delegated task completed (no output)")

	if !strings.Contains(resp, "func Restore()") {
		t.Errorf("partial output dropped on the Spawn-error path: %q", resp)
	}
	if !strings.Contains(resp, "model round timeout") {
		t.Errorf("stop reason not surfaced: %q", resp)
	}
}

// TestSpawnResponse_TrueSpawnFailureSurfacesError: an empty agentID means the
// agent never started — nothing to preserve, the spawn error itself is the
// only useful information.
func TestSpawnResponse_TrueSpawnFailureSurfacesError(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())

	resp := spawnResponse(runner, "", errors.New("could not construct agent"),
		"Delegation failed", "Delegated task completed (no output)")

	if !strings.Contains(resp, "could not construct agent") {
		t.Errorf("expected the spawn error surfaced, got %q", resp)
	}
}

// TestSpawnResponse_ResultErrorWithoutSpawnErrStillSurfaces: GetResult may
// carry a failure reason even when Spawn itself returned a nil err (the
// agent's own Run() failed internally after Spawn returned early results).
func TestSpawnResponse_ResultErrorWithoutSpawnErrStillSurfaces(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	runner.mu.Lock()
	runner.results["a2"] = &AgentResult{AgentID: "a2", Output: "", Error: "stalled mid-task"}
	runner.mu.Unlock()

	resp := spawnResponse(runner, "a2", nil, "Delegation failed", "Delegated task completed (no output)")
	if !strings.Contains(resp, "stalled mid-task") {
		t.Errorf("expected the result error surfaced, got %q", resp)
	}
}

// TestSpawnResponse_NoOutputNoErrorFallsBackToNoOutputMsg: a genuinely clean
// completion with no output text falls back to the caller-supplied message
// (e.g. "Delegated task completed (no output)"), unchanged from before.
func TestSpawnResponse_NoOutputNoErrorFallsBackToNoOutputMsg(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	runner.mu.Lock()
	runner.results["a3"] = &AgentResult{AgentID: "a3", Output: "", Error: ""}
	runner.mu.Unlock()

	resp := spawnResponse(runner, "a3", nil, "Delegation failed", "Delegated task completed (no output)")
	if resp != "Delegated task completed (no output)" {
		t.Errorf("got %q, want the noOutputMsg fallback", resp)
	}
}

// TestSpawnResponse_SuccessfulOutputWins: the common case — a clean success
// with real output and no error surfaces just the output.
func TestSpawnResponse_SuccessfulOutputWins(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	runner.mu.Lock()
	runner.results["a4"] = &AgentResult{AgentID: "a4", Output: "all done successfully"}
	runner.mu.Unlock()

	resp := spawnResponse(runner, "a4", nil, "Delegation failed", "Delegated task completed (no output)")
	if resp != "all done successfully" {
		t.Errorf("got %q, want the bare successful output", resp)
	}
}
