package router

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
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

// TestExecuteViaSubAgent_PreservesPartialWorkWhenSpawnReturnsError pins the REAL
// failure shape: agent.Run sets result.Error ONLY with a non-nil err, so a
// sub-agent that worked then failed returns Spawn=(agentID, err!=nil) — and the
// old code returned on `if err != nil` BEFORE GetResult, making the preserve-block
// structurally dead. The work must still be surfaced.
func TestExecuteViaSubAgent_PreservesPartialWorkWhenSpawnReturnsError(t *testing.T) {
	runner := &stubRunner{
		spawnID:  "a1",
		spawnErr: errors.New("model round timeout"),
		resultOK: true,
		result: &agent.AgentResult{
			Output: "Implemented restore():\n+func Restore() error { ... }",
			Error:  "model round timeout",
		},
	}
	r := &Router{agentRunner: runner}

	hist, resp, err := r.executeViaSubAgent(context.Background(), "build restore", "general", false)
	if err != nil {
		t.Fatalf("Spawn err with partial work must NOT end the turn: %v", err)
	}
	if !strings.Contains(resp, "func Restore()") {
		t.Errorf("partial work dropped on the Spawn-error path:\n%s", resp)
	}
	if !strings.Contains(resp, "model round timeout") {
		t.Errorf("stop reason should be surfaced:\n%s", resp)
	}
	if len(hist) == 0 {
		t.Error("history must carry the partial work forward")
	}
}

// TestExecuteViaSubAgent_TrueSpawnFailureErrors: a Spawn error with an EMPTY
// agentID means the agent never started — nothing to preserve, surface an error.
func TestExecuteViaSubAgent_TrueSpawnFailureErrors(t *testing.T) {
	runner := &stubRunner{spawnID: "", spawnErr: errors.New("could not construct agent")}
	r := &Router{agentRunner: runner}

	_, _, err := r.executeViaSubAgent(context.Background(), "x", "general", false)
	if err == nil {
		t.Fatal("a never-started spawn (empty agentID) must surface as an error")
	}
}

func TestExecuteViaSubAgent_StatefulFailureIsRetryUnsafeAndPreservesPartialMetadata(t *testing.T) {
	wantCause := errors.New("transient stream timeout")
	runner := &stubRunner{
		spawnID:  "stateful-agent",
		spawnErr: wantCause,
		resultOK: true,
		result: &agent.AgentResult{
			AgentID:              "stateful-agent",
			Output:               "edited config before the stream failed",
			Error:                wantCause.Error(),
			StatefulToolAttempts: 1,
			TouchedPaths:         []string{"config.yaml"},
		},
	}
	r := &Router{agentRunner: runner}

	history, response, err := r.executeViaSubAgent(
		context.Background(), "edit config", "general", false)
	if err == nil {
		t.Fatal("failed stateful run was converted to success")
	}
	if !errors.Is(err, wantCause) {
		t.Fatalf("typed error lost underlying cause: %v", err)
	}
	if !IsAutomaticRetryUnsafe(err) {
		t.Fatalf("stateful failure lacks retry-unsafe marker: %T %v", err, err)
	}
	var unsafeErr *AgentSideEffectError
	if !errors.As(err, &unsafeErr) {
		t.Fatalf("error type = %T, want *AgentSideEffectError", err)
	}
	if unsafeErr.AgentID != "stateful-agent" || unsafeErr.StatefulToolAttempts != 1 {
		t.Fatalf("unsafe provenance = %#v", unsafeErr)
	}
	if unsafeErr.PartialOutput != runner.result.Output || unsafeErr.Result == nil ||
		len(unsafeErr.Result.TouchedPaths) != 1 {
		t.Fatalf("partial result metadata was not preserved: %#v", unsafeErr)
	}
	if !strings.Contains(response, runner.result.Output) || len(history) != 1 {
		t.Fatalf("returned partial evidence = history:%d response:%q", len(history), response)
	}
}

func TestExecuteViaSubAgent_EmptyStatefulFailureStillBlocksRetry(t *testing.T) {
	runner := &stubRunner{
		spawnID:  "stateful-agent",
		spawnErr: errors.New("rate limited after tool result"),
		resultOK: true,
		result: &agent.AgentResult{
			AgentID:              "stateful-agent",
			Error:                "rate limited after tool result",
			StatefulToolAttempts: 1,
		},
	}
	r := &Router{agentRunner: runner}

	_, _, err := r.executeViaSubAgent(context.Background(), "mutate", "general", false)
	if err == nil || !IsAutomaticRetryUnsafe(err) {
		t.Fatalf("empty stateful failure = %T %v, want retry-unsafe error", err, err)
	}
}

func TestExecuteViaSubAgent_MissingStartedResultFailsClosed(t *testing.T) {
	runner := &stubRunner{spawnID: "started-agent", resultOK: false}
	r := &Router{agentRunner: runner}

	_, _, err := r.executeViaSubAgent(context.Background(), "work", "general", false)
	var unsafeErr *AgentSideEffectError
	if !errors.As(err, &unsafeErr) || !unsafeErr.SideEffectsUnknown ||
		!IsAutomaticRetryUnsafe(err) {
		t.Fatalf("missing started result = %T %#v, want unknown retry-unsafe provenance", err, unsafeErr)
	}
}

func TestExecute_StatefulFailureExtendsPartialHistoryWithoutBecomingSuccess(t *testing.T) {
	cause := errors.New("timeout after edit")
	runner := &stubRunner{
		spawnID: "stateful-agent", spawnErr: cause, resultOK: true,
		result: &agent.AgentResult{
			AgentID: "stateful-agent", Error: cause.Error(),
			Output: "partial implementation", StatefulToolAttempts: 1,
		},
	}
	mock := testkit.NewMockClient()
	r := NewRouter(&RouterConfig{
		Enabled: true, DecomposeThreshold: 100, ParallelThreshold: 100,
	}, nil, runner, mock, tools.NewRegistry(), false, t.TempDir())
	const prompt = "Refactor authentication across packages. Analyze every caller and update interfaces. Add migration tests and optimize error handling for all providers."
	if decision := r.Route(prompt); decision.Handler != HandlerSubAgent || decision.Background {
		t.Fatalf("test route = handler:%v background:%v", decision.Handler, decision.Background)
	}
	prior := []*genai.Content{
		genai.NewContentFromText("earlier question", genai.RoleUser),
		genai.NewContentFromText("earlier answer", genai.RoleModel),
	}

	history, response, err := r.Execute(context.Background(), prior, prompt)
	if err == nil || !IsAutomaticRetryUnsafe(err) {
		t.Fatalf("stateful failure = %T %v, want retry-unsafe error", err, err)
	}
	if len(history) != len(prior)+2 {
		t.Fatalf("extended history len = %d, want %d", len(history), len(prior)+2)
	}
	userTurn := history[len(prior)]
	modelTurn := history[len(prior)+1]
	if userTurn.Role != genai.RoleUser || len(userTurn.Parts) != 1 || userTurn.Parts[0].Text != prompt {
		t.Fatalf("partial history lost original user turn: %#v", userTurn)
	}
	if modelTurn.Role != genai.RoleModel || len(modelTurn.Parts) != 1 ||
		!strings.Contains(modelTurn.Parts[0].Text, "partial implementation") ||
		!strings.Contains(response, "partial implementation") {
		t.Fatalf("partial model output was not retained: turn=%#v response=%q", modelTurn, response)
	}
}

// TestExecuteSubtask_PreservesPartialWorkWhenSpawnReturnsError: the coordinated
// path's real failure shape (Spawn err) must also keep the partial output.
func TestExecuteSubtask_PreservesPartialWorkWhenSpawnReturnsError(t *testing.T) {
	runner := &stubRunner{
		spawnID:  "a1",
		spawnErr: errors.New("no progress"),
		resultOK: true,
		result:   &agent.AgentResult{Output: "partial parser code", Error: "no progress"},
	}
	r := &Router{agentRunner: runner}

	res := r.executeSubtask(context.Background(), Subtask{ID: "t1", AgentType: "general", Prompt: "build parser"})
	if res.Success {
		t.Error("failed subtask should be unsuccessful")
	}
	if res.Output != "partial parser code" {
		t.Errorf("partial subtask output dropped on Spawn-error path: %q", res.Output)
	}
	if res.AgentID != "a1" {
		t.Errorf("AgentID should be recorded even on failure, got %q", res.AgentID)
	}
}
