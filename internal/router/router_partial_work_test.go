package router

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/agent"
)

// stubRunner is a minimal AgentRunner for exercising the sub-agent failure
// paths without spawning a real agent.
type stubRunner struct {
	spawnID  string
	spawnErr error
	result   *agent.AgentResult
	resultOK bool
}

func (s *stubRunner) Spawn(_ context.Context, _ string, _ string, _ int, _ string) (string, error) {
	return s.spawnID, s.spawnErr
}
func (s *stubRunner) SpawnAsync(_ context.Context, _ string, _ string, _ int, _ string) string {
	return s.spawnID
}
func (s *stubRunner) GetResult(_ string) (*agent.AgentResult, bool) {
	return s.result, s.resultOK
}

// TestExecuteViaSubAgent_PreservesPartialWorkOnFailure pins that a sub-agent
// which did real work before failing surfaces that work (and an actionable
// reason) instead of the router discarding it and ending the turn with a bare
// error — the regression behind "agent worked 7 min then ✗ and the work vanished".
func TestExecuteViaSubAgent_PreservesPartialWorkOnFailure(t *testing.T) {
	runner := &stubRunner{
		spawnID:  "a1",
		resultOK: true,
		result: &agent.AgentResult{
			Output: "Built the backup command:\n+func Backup() error { ... }",
			Error:  "model round timeout",
		},
	}
	r := &Router{agentRunner: runner}

	hist, resp, err := r.executeViaSubAgent(context.Background(), "build backup", "general", false)
	if err != nil {
		t.Fatalf("partial work present → must NOT propagate a turn-ending error: %v", err)
	}
	if !strings.Contains(resp, "func Backup()") {
		t.Errorf("partial work was dropped from the response:\n%s", resp)
	}
	if !strings.Contains(resp, "model round timeout") {
		t.Errorf("the stop reason should be surfaced:\n%s", resp)
	}
	if len(hist) == 0 {
		t.Error("history must carry the partial work forward so the turn can continue")
	}
}

// TestExecuteViaSubAgent_ErrorsOnlyWhenNoPartialWork: a failure with genuinely
// nothing produced still surfaces as an error (there's nothing to continue from).
func TestExecuteViaSubAgent_ErrorsOnlyWhenNoPartialWork(t *testing.T) {
	runner := &stubRunner{
		spawnID:  "a1",
		resultOK: true,
		result:   &agent.AgentResult{Output: "", Error: "spawn died immediately"},
	}
	r := &Router{agentRunner: runner}

	_, _, err := r.executeViaSubAgent(context.Background(), "x", "general", false)
	if err == nil {
		t.Fatal("a truly-empty failure should still surface as an error")
	}
}

// TestExecuteSubtask_PreservesPartialWorkOnFailure pins the coordinated-path fix:
// a failed subtask keeps its partial Output (previously dropped).
func TestExecuteSubtask_PreservesPartialWorkOnFailure(t *testing.T) {
	runner := &stubRunner{
		spawnID:  "a1",
		resultOK: true,
		result:   &agent.AgentResult{Output: "partial restore command code", Error: "stalled"},
	}
	r := &Router{agentRunner: runner}

	res := r.executeSubtask(context.Background(), Subtask{ID: "t1", AgentType: "general", Prompt: "build restore"})
	if res.Success {
		t.Error("a failed subtask should be marked unsuccessful")
	}
	if res.Output != "partial restore command code" {
		t.Errorf("partial subtask output was dropped: %q", res.Output)
	}
	if res.Error != "stalled" {
		t.Errorf("error reason = %q, want 'stalled'", res.Error)
	}
}
