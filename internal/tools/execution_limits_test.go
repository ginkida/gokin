package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

func TestContextWithMaxTurnsIsInvocationScoped(t *testing.T) {
	base := context.Background()
	limited := ContextWithMaxTurns(base, 7)

	if got, ok := MaxTurnsFromContext(limited); !ok || got != 7 {
		t.Fatalf("limited context = %d, %v", got, ok)
	}
	if got, ok := MaxTurnsFromContext(base); ok || got != 0 {
		t.Fatalf("base context was mutated: %d, %v", got, ok)
	}
	if got, ok := MaxTurnsFromContext(ContextWithMaxTurns(base, 0)); !ok || got != 0 {
		t.Fatalf("zero should explicitly disable the cap: %d, %v", got, ok)
	}
}

func TestExecutorMaxTurnsSharedAcrossContinuationAndRetry(t *testing.T) {
	cl := &scriptedExecutorClient{
		model: "glm-5.3",
		responses: []*client.StreamingResponse{
			buildExecutorTestTextStream("first answer"),
			buildExecutorTestTextStream("continuation answer"),
			buildExecutorTestTextStream("new invocation"),
		},
	}
	executor := NewExecutor(NewRegistry(), cl, time.Second)
	ctx := ContextWithMaxTurns(t.Context(), 2)
	history, _, err := executor.Execute(ctx, nil, "start")
	if err != nil {
		t.Fatal(err)
	}
	history, response, err := executor.ExecuteContinuation(ctx, history, "continue")
	if err != nil || response != "continuation answer" {
		t.Fatalf("allowed continuation=%q err=%v", response, err)
	}
	_, _, err = executor.Execute(ctx, history, "retry same invocation")
	if !errors.Is(err, ErrMaxTurnsExceeded) || executor.GetLastModelTurns() != 0 {
		t.Fatalf("re-entry regained turns: err=%v rounds=%d", err, executor.GetLastModelTurns())
	}
	_, response, err = executor.Execute(ContextWithMaxTurns(t.Context(), 1), nil, "new prompt")
	if err != nil || response != "new invocation" {
		t.Fatalf("cap leaked or consumed a refused provider call: response=%q err=%v", response, err)
	}
}

func TestExecutorMaxTurnsFailsClosedAndDoesNotLeak(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatal(err)
	}
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("limited-read"),
			buildExecutorTestTextStream("healthy next invocation"),
		},
	}
	executor := NewExecutor(registry, cl, time.Second)

	limitedHistory, partial, err := executor.Execute(
		ContextWithMaxTurns(context.Background(), 1), nil, "inspect then continue")
	if !errors.Is(err, ErrMaxTurnsExceeded) {
		t.Fatalf("limited execution error = %v, want ErrMaxTurnsExceeded", err)
	}
	var limitErr *MaxTurnsExceededError
	if !errors.As(err, &limitErr) || limitErr.Limit != 1 {
		t.Fatalf("typed limit error = %#v", err)
	}
	if !strings.Contains(partial, "INCOMPLETE") {
		t.Fatalf("partial result lacks incomplete marker: %q", partial)
	}
	if readTool.calls != 1 {
		t.Fatalf("tool calls = %d, want 1", readTool.calls)
	}
	if len(limitedHistory) < 4 ||
		limitedHistory[len(limitedHistory)-2].Role != genai.RoleUser ||
		limitedHistory[len(limitedHistory)-1].Role != genai.RoleModel {
		t.Fatalf("limited history does not close tool results with a model turn: %+v", limitedHistory)
	}

	_, second, err := executor.Execute(context.Background(), nil, "answer normally")
	if err != nil || second != "healthy next invocation" {
		t.Fatalf("limit leaked into next invocation: response=%q err=%v", second, err)
	}
}

func TestExecutorZeroMaxTurnsAllowsHeadlessWorkBeyondAdaptiveBudget(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatal(err)
	}
	responses := make([]*client.StreamingResponse, 0, 22)
	for i := range 21 {
		responses = append(responses,
			buildExecutorTestReadOffsetStream("page", "large.go", 1+i*2000, 2000))
	}
	responses = append(responses, buildExecutorTestTextStream("all pages inspected"))
	cl := &scriptedExecutorClient{model: "glm-5.2", responses: responses}
	executor := NewExecutor(registry, cl, time.Second)

	_, response, err := executor.Execute(
		ContextWithMaxTurns(context.Background(), 0), nil, "inspect every page")
	if err != nil || response != "all pages inspected" {
		t.Fatalf("uncapped execution response=%q err=%v", response, err)
	}
	if readTool.calls != 21 {
		t.Fatalf("tool calls = %d, want 21 (> adaptive base of 20)", readTool.calls)
	}
}
