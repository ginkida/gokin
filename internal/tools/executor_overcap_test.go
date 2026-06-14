package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type fastTool struct{ name string }

func (f *fastTool) Name() string                  { return f.name }
func (f *fastTool) Description() string           { return "test-only fast tool" }
func (f *fastTool) Validate(map[string]any) error { return nil }
func (f *fastTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: f.name}
}
func (f *fastTool) Execute(context.Context, map[string]any) (ToolResult, error) {
	return NewSuccessResult("ran"), nil
}

// TestExecuteTools_OverCapPairsEveryCall pins the headline Tier-0 fix: when the
// model emits more than MaxFunctionCallsPerResponse calls, every emitted call
// must still get exactly one paired result (with its original ID), or
// Anthropic-compat providers 400 on the orphaned tool_use and the turn fails.
// Overflow calls beyond the cap get an explicit "not executed" result rather
// than being silently dropped.
func TestExecuteTools_OverCapPairsEveryCall(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&fastTool{name: "fast_tool"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	exec := NewExecutor(registry, &scriptedExecutorClient{model: "glm-5.1"}, time.Second)

	n := MaxFunctionCallsPerResponse + 5
	calls := make([]*genai.FunctionCall, n)
	for i := range calls {
		calls[i] = &genai.FunctionCall{ID: fmt.Sprintf("c%d", i), Name: "fast_tool", Args: map[string]any{}}
	}

	results, err := exec.executeTools(context.Background(), calls)
	if err != nil {
		t.Fatalf("executeTools error = %v", err)
	}

	// (a) every call paired.
	if len(results) != n {
		t.Fatalf("len(results)=%d, want %d — every emitted call must be paired", len(results), n)
	}
	// (b) IDs match input order (no orphan, no reorder).
	for i, r := range results {
		if r.ID != calls[i].ID {
			t.Errorf("results[%d].ID=%q, want %q — pairing/order broken", i, r.ID, calls[i].ID)
		}
	}
	// (c) first cap executed.
	for i := 0; i < MaxFunctionCallsPerResponse; i++ {
		if msg, _ := results[i].Response["error"].(string); strings.Contains(msg, "not executed") {
			t.Errorf("results[%d] should have executed, got not-executed", i)
		}
	}
	// (d) overflow carries an explicit "not executed" result.
	for i := MaxFunctionCallsPerResponse; i < n; i++ {
		msg, _ := results[i].Response["error"].(string)
		if !strings.Contains(msg, "not executed") {
			t.Errorf("results[%d] (overflow) should be 'not executed', got %v", i, results[i].Response)
		}
	}
}
