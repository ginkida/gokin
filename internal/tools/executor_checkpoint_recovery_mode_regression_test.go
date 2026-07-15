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

	// The same call in an explicit retry window must still be deduplicated.
	executor.SetSideEffectDedup(true)
	recovered := executor.doExecuteTool(context.Background(), &genai.FunctionCall{
		ID: "call-3", Name: "bash", Args: args,
	})
	if got := tool.calls.Load(); got != 2 {
		t.Fatalf("recovery execution count = %d, want 2; duplicate side effect was re-executed", got)
	}
	if recovered.Content != second.Content {
		t.Fatalf("recovered content = %q, want latest result %q", recovered.Content, second.Content)
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
