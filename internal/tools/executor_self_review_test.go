package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

type selfReviewMutationTool struct {
	name         string
	writtenPaths []string
}

func (t *selfReviewMutationTool) Name() string { return t.name }

func (t *selfReviewMutationTool) Description() string { return "self-review mutation test tool" }

func (t *selfReviewMutationTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.name, Description: t.Description()}
}

func (t *selfReviewMutationTool) Validate(args map[string]any) error { return nil }

func (t *selfReviewMutationTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	if len(t.writtenPaths) == 0 {
		return NewSuccessResult("updated"), nil
	}
	return NewSuccessResultWithData("updated", map[string]any{
		"written_paths": append([]string(nil), t.writtenPaths...),
	}), nil
}

func TestExecutorSelfReview_AccumulatesAcrossRoundsAndCountsDeclaredAndBatchPaths(t *testing.T) {
	registry := NewRegistry()
	for _, tool := range []Tool{
		&selfReviewMutationTool{name: "refactor", writtenPaths: []string{"a.go"}},
		&selfReviewMutationTool{name: "batch", writtenPaths: []string{"b.go", "c.go"}},
	} {
		if err := registry.Register(tool); err != nil {
			t.Fatalf("Register(%s) error = %v", tool.Name(), err)
		}
	}

	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildSelfReviewToolStream("r1", "refactor", map[string]any{"pattern": "**/*.go"}),
			buildSelfReviewToolStream("r2", "batch", map[string]any{"files": []any{"b.go", "c.go"}}),
			buildExecutorTestTextStream("done"),
		},
	}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	exec.SetDeltaCheckConfig(false, 0, false, 0)
	exec.SetSelfReviewThreshold(3)

	if _, _, err := exec.Execute(context.Background(), nil, "change three files"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(cl.functionResults) != 2 {
		t.Fatalf("function-result rounds = %d, want 2", len(cl.functionResults))
	}
	if first := selfReviewResponseText(cl.functionResults[0]); strings.Contains(first, "[smart-validation]") {
		t.Fatalf("first round injected self-review below threshold: %q", first)
	}
	second := selfReviewResponseText(cl.functionResults[1])
	for _, want := range []string{"[smart-validation]", "a.go", "b.go", "c.go"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second-round response = %q, want it to contain %q", second, want)
		}
	}
}

func TestExecutorSelfReview_ResetsAtTopLevelRequestBoundary(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&selfReviewMutationTool{name: "edit"}); err != nil {
		t.Fatalf("Register(edit) error = %v", err)
	}
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildSelfReviewToolStream("e1", "edit", map[string]any{"file_path": "fresh.go"}),
			buildExecutorTestTextStream("done"),
		},
	}
	exec := NewExecutor(registry, cl, time.Second)
	exec.preFlightChecks = false
	exec.SetDeltaCheckConfig(false, 0, false, 0)
	exec.SetSelfReviewThreshold(2)

	// Simulate residual bookkeeping from a prior request. The app calls
	// ResetSideEffectLedger at the real user-request boundary.
	exec.mutatedFilesMu.Lock()
	exec.mutatedFilesThisTurn["stale.go"] = true
	exec.mutatedFilesMu.Unlock()
	exec.ResetSideEffectLedger()

	if _, _, err := exec.Execute(context.Background(), nil, "change one file"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(cl.functionResults) != 1 {
		t.Fatalf("function-result rounds = %d, want 1", len(cl.functionResults))
	}
	if got := selfReviewResponseText(cl.functionResults[0]); strings.Contains(got, "[smart-validation]") {
		t.Fatalf("stale path crossed top-level Execute boundary: %q", got)
	}

	exec.mutatedFilesMu.Lock()
	defer exec.mutatedFilesMu.Unlock()
	if len(exec.mutatedFilesThisTurn) != 1 || !exec.mutatedFilesThisTurn["fresh.go"] {
		t.Fatalf("mutated files = %v, want only fresh.go", exec.mutatedFilesThisTurn)
	}
}

func TestExecutorSelfReview_DoesNotResetAcrossExecuteRetries(t *testing.T) {
	registry := NewRegistry()
	cl := &scriptedExecutorClient{model: "glm-5.2", responses: []*client.StreamingResponse{buildExecutorTestTextStream("done")}}
	exec := NewExecutor(registry, cl, time.Second)
	exec.SetSelfReviewThreshold(2)
	exec.ResetSideEffectLedger()

	// A provider retry invokes Execute again without crossing the request
	// boundary. Mutations from the prior attempt must remain available because
	// its side-effect checkpoints remain available too.
	exec.mutatedFilesMu.Lock()
	exec.mutatedFilesThisTurn["attempt-one.go"] = true
	exec.mutatedFilesMu.Unlock()

	if _, _, err := exec.Execute(context.Background(), nil, "retry"); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	exec.mutatedFilesMu.Lock()
	defer exec.mutatedFilesMu.Unlock()
	if !exec.mutatedFilesThisTurn["attempt-one.go"] {
		t.Fatalf("retry cleared prior-attempt mutation bookkeeping: %v", exec.mutatedFilesThisTurn)
	}
}

func TestSelfReviewMutationPaths_PrefersActualWritesAndSuppressesNoOps(t *testing.T) {
	call := &genai.FunctionCall{Name: "batch", Args: map[string]any{"files": []any{"requested-a.go", "requested-b.go"}}}
	if got := selfReviewMutationPaths(call, goMutationSnapshot{PathsDeclared: true}); len(got) != 0 {
		t.Fatalf("no-op batch paths = %v, want none", got)
	}
	got := selfReviewMutationPaths(call, goMutationSnapshot{
		PathsDeclared: true,
		Paths:         []string{"./actual.go", "actual.go", ""},
	})
	if len(got) != 1 || got[0] != "actual.go" {
		t.Fatalf("actual paths = %v, want one normalized path", got)
	}
	if got := selfReviewMutationPaths(
		&genai.FunctionCall{Name: "delete", Args: map[string]any{"path": "old.go"}},
		goMutationSnapshot{ChangedKnown: true, Changed: false},
	); len(got) != 0 {
		t.Fatalf("changed=false paths = %v, want none", got)
	}
}

func buildSelfReviewToolStream(id, name string, args map[string]any) *client.StreamingResponse {
	return buildExecutorTestStream(client.ResponseChunk{
		FunctionCalls: []*genai.FunctionCall{{ID: id, Name: name, Args: args}},
		Done:          true,
		FinishReason:  genai.FinishReasonStop,
	})
}

func selfReviewResponseText(results []*genai.FunctionResponse) string {
	if len(results) == 0 || results[len(results)-1] == nil {
		return ""
	}
	response := results[len(results)-1].Response
	content, _ := response["content"].(string)
	errorText, _ := response["error"].(string)
	return content + "\n" + errorText
}
