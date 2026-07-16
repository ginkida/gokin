package tools

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

// TestExecutorCheckpointReplayRequiresRecoveryMode pins a foreground reliability
// invariant: a checkpoint is a retry/recovery mechanism, not a general-purpose
// cache for mutating tools. In a healthy turn the model may legitimately run the
// same bash/write operation again after intervening edits (most importantly, a
// second verification command). Replaying the first result would skip the real
// execution and let stale green output masquerade as current verification.
func TestExecutorCheckpointReplayRequiresRecoveryMode(t *testing.T) {
	registry := NewRegistry()
	tool := &checkpointCountingBashTool{}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register bash tool: %v", err)
	}

	executor := NewExecutor(registry, nil, time.Second)
	executor.EnablePreFlightChecks(false)
	args := map[string]any{
		"command": "go test ./...",
	}

	first := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "call-1", Name: "bash", Args: args,
	})
	second := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "call-2", Name: "bash", Args: args,
	})

	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("normal execution count = %d, want 2; checkpoint replay skipped a legitimate repeat", got)
	}
	if first.Content == second.Content {
		t.Fatalf("second normal execution reused stale result %q", second.Content)
	}

	// An explicit retry replays repeated identical calls in their original
	// order, rather than collapsing them to the last result.
	executor.SetSideEffectDedup(true)
	recovered := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "call-3", Name: "bash", Args: args,
	})
	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("recovery execution count = %d, want 2; duplicate side effect was re-executed", got)
	}
	if recovered.Content != first.Content {
		t.Fatalf("recovered content = %q, want first ordered result %q", recovered.Content, first.Content)
	}

	recoveredSecond := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "call-4", Name: "bash", Args: args,
	})
	if got := tool.calls.Load(); got != 2 || recoveredSecond.Content != second.Content {
		t.Fatalf("second ordered replay = %q calls=%d, want %q with 2 calls",
			recoveredSecond.Content, got, second.Content)
	}

	// Every saved occurrence is one-shot. Only after both are exhausted is the
	// same command new work and allowed to execute normally.
	again := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "call-5", Name: "bash", Args: args,
	})
	if got := tool.calls.Load(); got != 3 {
		t.Fatalf("post-replay execution count = %d, want 3; replay leaked across the turn", got)
	}
	if again.Content == recovered.Content || again.Content == recoveredSecond.Content {
		t.Fatalf("post-replay command reused stale result %q", again.Content)
	}

	// A journal restored without the in-memory ledger remains usable once the
	// caller explicitly enters recovery mode (the cross-process resume seam).
	restoredRegistry := NewRegistry()
	restoredTool := &checkpointCountingBashTool{}
	if err := restoredRegistry.Register(restoredTool); err != nil {
		t.Fatalf("register restored bash tool: %v", err)
	}
	restoredExecutor := NewExecutor(restoredRegistry, nil, time.Second)
	restoredExecutor.EnablePreFlightChecks(false)
	restoredExecutor.GetCheckpointJournal().Record(&genai.FunctionCall{
		ID: "persisted-call", Name: "bash", Args: args,
	}, NewSuccessResult("persisted-result"))
	restoredExecutor.SetSideEffectDedup(true)
	fromJournal := restoredExecutor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "new-call-id", Name: "bash", Args: args,
	})
	if got := restoredTool.calls.Load(); got != 0 {
		t.Fatalf("restored checkpoint executed tool %d times, want 0", got)
	}
	if fromJournal.Content != "persisted-result" {
		t.Fatalf("restored checkpoint content = %q, want persisted-result", fromJournal.Content)
	}
	postJournal := restoredExecutor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "post-journal", Name: "bash", Args: args,
	})
	if got := restoredTool.calls.Load(); got != 1 {
		t.Fatalf("restored replay was not one-shot: executions=%d", got)
	}
	if postJournal.Content == fromJournal.Content {
		t.Fatalf("post-journal execution reused stale result %q", postJournal.Content)
	}
}

func TestExecutorRecoveryReplaysDuplicateSignaturesAsOrderedMultiset(t *testing.T) {
	registry := NewRegistry()
	tool := &checkpointCountingBashTool{}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register bash tool: %v", err)
	}
	executor := NewExecutor(registry, nil, time.Second)
	executor.EnablePreFlightChecks(false)
	args := map[string]any{"command": "go test ./..."}

	first := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "original-1", Name: "bash", Args: args,
	})
	second := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "original-2", Name: "bash", Args: args,
	})
	if first.Content == second.Content {
		t.Fatalf("fixture did not produce distinct ordered outcomes: %q", first.Content)
	}

	executor.SetSideEffectDedup(true)
	replayedFirst := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "regenerated-1", Name: "bash", Args: args,
	})
	replayedSecond := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "regenerated-2", Name: "bash", Args: args,
	})
	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("ordered replay executed the tool: calls=%d, want 2", got)
	}
	if replayedFirst.Content != first.Content || replayedSecond.Content != second.Content {
		t.Fatalf("ordered replay = (%q, %q), want (%q, %q)",
			replayedFirst.Content, replayedSecond.Content, first.Content, second.Content)
	}

	third := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "regenerated-3", Name: "bash", Args: args,
	})
	if got := tool.calls.Load(); got != 3 {
		t.Fatalf("exhausted replay reused a checkpoint: calls=%d, want 3", got)
	}
	if third.Content == first.Content || third.Content == second.Content {
		t.Fatalf("new mutation reused an exhausted outcome %q", third.Content)
	}
}

func TestExecutorDivergentMutationBlockedUntilReplayExhausted(t *testing.T) {
	registry := NewRegistry()
	tool := &checkpointCountingBashTool{}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register bash tool: %v", err)
	}
	executor := NewExecutor(registry, nil, time.Second)
	executor.EnablePreFlightChecks(false)

	argsA := map[string]any{"command": "mutate-a"}
	argsB := map[string]any{"command": "mutate-b"}
	argsC := map[string]any{"command": "new-mutation-c"}
	originalA := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "original-a", Name: "bash", Args: argsA,
	})
	originalB := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "original-b", Name: "bash", Args: argsB,
	})

	executor.SetSideEffectDedup(true)
	blockedC := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		// Reusing A's ID with different arguments must not bypass the
		// signature check or consume A's checkpoint.
		ID: "original-a", Name: "bash", Args: argsC,
	})
	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("divergent mutation executions = %d, want 2 total", got)
	}
	if blockedC.Success || blockedC.PolicyBlock == nil || blockedC.PolicyBlock.Kind != PolicyBlockSafety {
		t.Fatalf("divergent mutation result = %+v, want safety policy block", blockedC)
	}
	if got := executor.GetCheckpointJournal().ReplayRemaining(); got != 2 {
		t.Fatalf("divergence consumed replay entries: remaining=%d, want 2", got)
	}

	replayedA := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "original-a", Name: "bash", Args: argsA,
	})
	replayedB := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		// A regenerated ID exercises the ordered signature path.
		ID: "regenerated-b", Name: "bash", Args: argsB,
	})
	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("replay re-executed a completed mutation: calls=%d, want 2", got)
	}
	if replayedA.Content != originalA.Content || replayedB.Content != originalB.Content {
		t.Fatalf("replay after blocked divergence = (%q, %q), want (%q, %q)",
			replayedA.Content, replayedB.Content, originalA.Content, originalB.Content)
	}
	if got := executor.GetCheckpointJournal().ReplayRemaining(); got != 0 {
		t.Fatalf("replay remaining after A/B = %d, want 0", got)
	}

	newC := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "new-c", Name: "bash", Args: argsC,
	})
	if got := tool.calls.Load(); got != 3 {
		t.Fatalf("post-replay execution count = %d, want 3 total", got)
	}
	if !newC.Success || newC.Content != "execution-3" {
		t.Fatalf("post-replay mutation result = %+v, want execution-3", newC)
	}
}

func TestExecutorReplayRejectsReusedCallIDWithChangedArguments(t *testing.T) {
	registry := NewRegistry()
	tool := &checkpointCountingBashTool{}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register bash tool: %v", err)
	}
	executor := NewExecutor(registry, nil, time.Second)
	executor.EnablePreFlightChecks(false)

	first := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "reused-id", Name: "bash", Args: map[string]any{"command": "first"},
	})
	executor.SetSideEffectDedup(true)

	changed := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "reused-id", Name: "bash", Args: map[string]any{"command": "second"},
	})
	if got := tool.calls.Load(); got != 1 {
		t.Fatalf("changed call executed %d times, want 1 total; divergent replay was not blocked", got)
	}
	if changed.Success || changed.PolicyBlock == nil || changed.PolicyBlock.Kind != PolicyBlockSafety {
		t.Fatalf("changed call result = %+v, want safety policy block", changed)
	}
	if got := executor.GetCheckpointJournal().ReplayRemaining(); got != 1 {
		t.Fatalf("changed-ID divergence consumed checkpoint: remaining=%d, want 1", got)
	}

	replayed := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "reused-id", Name: "bash", Args: map[string]any{"command": "first"},
	})
	if got := tool.calls.Load(); got != 1 || replayed.Content != first.Content {
		t.Fatalf("exact-ID replay = %+v calls=%d, want original with 1 call", replayed, got)
	}

	afterExhaustion := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "reused-id", Name: "bash", Args: map[string]any{"command": "second"},
	})
	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("post-exhaustion changed call executions = %d, want 2 total", got)
	}
	if !afterExhaustion.Success || afterExhaustion.Content == first.Content {
		t.Fatalf("post-exhaustion changed call reused stale result: %+v", afterExhaustion)
	}
}

func TestExecutorPrepareRecoveryUsesCapturedGeneration(t *testing.T) {
	registry := NewRegistry()
	tool := &checkpointCountingBashTool{}
	if err := registry.Register(tool); err != nil {
		t.Fatalf("register bash tool: %v", err)
	}
	executor := NewExecutor(registry, nil, time.Second)
	executor.EnablePreFlightChecks(false)

	original := &genai.FunctionCall{
		ID: "original", Name: "bash", Args: map[string]any{"command": "mutate-a"},
	}
	want := executor.doExecuteTool(context.Background(), original)
	captured := executor.GetCheckpointJournal().Entries()

	// Simulate an intervening fresh turn replacing the live ledger/journal.
	executor.ResetSideEffectLedger()
	executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "intervening", Name: "bash", Args: map[string]any{"command": "mutate-b"},
	})
	executor.PrepareSideEffectRecovery(captured)

	got := executor.doExecuteTool(context.Background(), original)
	if calls := tool.calls.Load(); calls != 2 {
		t.Fatalf("captured recovery re-executed original mutation: calls=%d, want 2", calls)
	}
	if got.Success != want.Success || got.Content != want.Content {
		t.Fatalf("recovered result = %+v, want %+v", got, want)
	}
}

type checkpointCountingBashTool struct {
	calls atomic.Int32
}

func (t *checkpointCountingBashTool) Name() string { return "bash" }

func (t *checkpointCountingBashTool) Description() string { return "counting bash test tool" }

func (t *checkpointCountingBashTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name(), Description: t.Description()}
}

func (t *checkpointCountingBashTool) Validate(map[string]any) error { return nil }

func (t *checkpointCountingBashTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	n := t.calls.Add(1)
	return NewSuccessResult(fmt.Sprintf("execution-%d", n)), nil
}
