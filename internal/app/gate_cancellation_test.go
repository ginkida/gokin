package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/donegate"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// A canceled context (user Esc / deadline) during the END-OF-TURN gates must
// PRESERVE the work the agent already finished, not discard the turn behind a
// "completion review failed / done-gate blocked" error card. These pin the
// field-report fix ("general ✗ 7.1m … completion review failed: context
// canceled" vanished minutes of completed work).

func TestRunCompletionReviewIfNeeded_CanceledContextPreservesWork(t *testing.T) {
	mock := testkit.NewMockClient()
	registry := tools.NewRegistry()
	exec := tools.NewExecutor(registry, mock, time.Second)
	a := &App{
		workDir:  testkit.ResolvedTempDir(t),
		executor: exec,
		session:  chat.NewSession(),
	}
	a.responseToolsUsed = []string{"edit"}             // a code edit with...
	a.responseTouchedPaths = []string{"controller.go"} // ...a .go file and...
	// ...no verification proof → the review WOULD normally run. Guard the
	// precondition so a heuristic change can't silently make this a no-op pass.
	if !donegate.ShouldRunCompletionReview("implement the controller", "I implemented the controller.",
		a.responseToolsUsed, a.responseTouchedPaths, nil) {
		t.Fatal("precondition: ShouldRunCompletionReview should be true for this setup")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // turn interrupted before the optional review

	resp := "I implemented the controller."
	in, out, cache := 0, 0, 0
	cost, costTracked := 0.0, false
	ok := a.runCompletionReviewIfNeeded(ctx, "implement the controller", &resp, &in, &out, &cache, &cost, &costTracked)

	if !ok {
		t.Fatal("canceled context must preserve work (return true), not discard the turn")
	}
	if resp != "I implemented the controller." {
		t.Fatalf("response must be unchanged, got %q", resp)
	}
	if n := len(mock.Calls()); n != 0 {
		t.Fatalf("no model call should happen on a canceled review, got %d", n)
	}
}

func TestEnforceDoneGate_CanceledContextSkipsInsteadOfBlocking(t *testing.T) {
	// config nil → doneGatePolicy().Enabled defaults true, so the gate is live.
	a := &App{workDir: testkit.ResolvedTempDir(t)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if !a.enforceDoneGate(ctx, "fix the parser bug in internal/parse.go") {
		t.Fatal("canceled context must SKIP the done-gate (return true), not block finalization")
	}
}
