package tools

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"

	"google.golang.org/genai"
)

func TestContextWithMaxBudgetUSDIsInvocationScoped(t *testing.T) {
	base := context.Background()
	limited := ContextWithMaxBudgetUSD(base, 1.25)

	if got, ok := MaxBudgetUSDFromContext(limited); !ok || got != 1.25 {
		t.Fatalf("limited budget = %v, %v", got, ok)
	}
	if got, ok := MaxBudgetUSDFromContext(base); ok || got != 0 {
		t.Fatalf("base context was mutated: %v, %v", got, ok)
	}
	if _, ok := MaxBudgetUSDFromContext(ContextWithMaxBudgetUSD(base, 0)); ok {
		t.Fatal("zero budget should disable enforcement")
	}
}

func TestInvocationBudgetLedgerRoundLeaseIsCancellableAndReusable(t *testing.T) {
	ctx := ContextWithMaxBudgetUSD(context.Background(), 1)
	ledger, ok := InvocationBudgetLedgerFromContext(ctx)
	if !ok {
		t.Fatal("budget ledger missing")
	}
	if err := ledger.BeginRound(ctx); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := ledger.BeginRound(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended BeginRound() error = %v, want deadline", err)
	}
	ledger.EndRound()
	if err := ledger.BeginRound(ctx); err != nil {
		t.Fatalf("ledger was not reusable after cancelled waiter: %v", err)
	}
	ledger.EndRound()
}

func TestInvocationBudgetLedgerConcurrentSpendIsExact(t *testing.T) {
	ctx := ContextWithMaxBudgetUSD(context.Background(), 100)
	ledger, _ := InvocationBudgetLedgerFromContext(ctx)
	const workers = 32
	const additions = 100
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range additions {
				ledger.AddSpend(0.01)
			}
		}()
	}
	wg.Wait()
	_, spent := ledger.Snapshot()
	want := float64(workers*additions) * 0.01
	if diff := spent - want; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("concurrent spend = %.12f, want %.12f", spent, want)
	}
}

func TestExecutorBudgetRequiresPricingBeforeFirstProviderRequest(t *testing.T) {
	cl := &scriptedExecutorClient{
		model:     "unknown-model",
		responses: []*client.StreamingResponse{buildExecutorTestTextStream("must remain queued")},
	}
	executor := NewExecutor(NewRegistry(), cl, time.Second)

	history, partial, err := executor.Execute(
		ContextWithMaxBudgetUSD(context.Background(), 0.50), nil, "do not spend")
	if !errors.Is(err, ErrCostUnavailable) {
		t.Fatalf("Execute() error = %v, want ErrCostUnavailable", err)
	}
	var unavailable *CostUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Model != "unknown-model" {
		t.Fatalf("typed cost error = %#v", err)
	}
	if cl.next != 0 {
		t.Fatalf("provider calls = %d, want 0", cl.next)
	}
	if !strings.Contains(partial, "cost tracking unavailable") {
		t.Fatalf("partial result = %q", partial)
	}
	if len(history) != 2 || history[0].Role != genai.RoleUser || history[1].Role != genai.RoleModel {
		t.Fatalf("preflight history = %+v", history)
	}
}

func TestExecutorBudgetStopsPendingToolsWithoutSideEffects(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatal(err)
	}
	cl := &scriptedExecutorClient{
		model:     "glm-5.2",
		responses: []*client.StreamingResponse{buildExecutorTestReadStream("blocked-read")},
	}
	executor := NewExecutor(registry, cl, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.60, true
	})

	history, partial, err := executor.Execute(
		ContextWithMaxBudgetUSD(context.Background(), 0.50), nil, "inspect")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Execute() error = %v, want ErrBudgetExceeded", err)
	}
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) ||
		math.Abs(budgetErr.LimitUSD-0.50) > 1e-12 ||
		math.Abs(budgetErr.SpentUSD-0.60) > 1e-12 {
		t.Fatalf("typed budget error = %#v", err)
	}
	if readTool.calls != 0 {
		t.Fatalf("read calls = %d, want 0", readTool.calls)
	}
	if cl.next != 1 {
		t.Fatalf("provider calls = %d, want 1", cl.next)
	}
	if !strings.Contains(partial, "INCOMPLETE") {
		t.Fatalf("partial result = %q", partial)
	}
	assertBudgetHistoryClosesPendingCall(t, history, "blocked-read")
}

func TestExecutorBudgetAccumulatesEveryProviderRound(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatal(err)
	}
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestReadStream("paid-read"),
			buildExecutorTestTextStream("completed response"),
		},
	}
	executor := NewExecutor(registry, cl, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.60, true
	})

	_, _, err := executor.Execute(
		ContextWithMaxBudgetUSD(context.Background(), 1.00), nil, "inspect and answer")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Execute() error = %v, want ErrBudgetExceeded", err)
	}
	if readTool.calls != 1 {
		t.Fatalf("read calls = %d, want 1", readTool.calls)
	}
	if cl.next != 2 {
		t.Fatalf("provider calls = %d, want 2", cl.next)
	}
	cost, tracked := executor.GetLastEstimatedCost()
	if !tracked || math.Abs(cost-1.20) > 1e-12 {
		t.Fatalf("cost = %v tracked=%v, want 1.20 true", cost, tracked)
	}
}

func TestExecutorBudgetWinsWhenPartialErroredResponseCrossesCeiling(t *testing.T) {
	registry := NewRegistry()
	readTool := &scriptedReadTool{}
	if err := registry.Register(readTool); err != nil {
		t.Fatal(err)
	}
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{buildExecutorTestStream(
			client.ResponseChunk{
				FunctionCalls: []*genai.FunctionCall{{
					ID:   "partial-read",
					Name: "read",
					Args: map[string]any{"file_path": "project.go"},
				}},
				InputTokens:  100,
				OutputTokens: 10,
			},
			client.ResponseChunk{Error: errors.New("stream broke"), Done: true},
		)},
	}
	executor := NewExecutor(registry, cl, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.60, true
	})

	history, partial, err := executor.Execute(
		ContextWithMaxBudgetUSD(context.Background(), 0.50), nil, "inspect")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Execute() error = %v, want ErrBudgetExceeded", err)
	}
	if readTool.calls != 0 {
		t.Fatalf("read calls = %d, want 0", readTool.calls)
	}
	if !strings.Contains(partial, "INCOMPLETE") {
		t.Fatalf("partial result = %q", partial)
	}
	assertBudgetHistoryClosesPendingCall(t, history, "partial-read")
}

func TestExecutorBudgetAllowsExactFinalSpendAndDoesNotLeak(t *testing.T) {
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestTextStream("exactly affordable"),
			buildExecutorTestTextStream("unbudgeted next turn"),
		},
	}
	executor := NewExecutor(NewRegistry(), cl, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.50, true
	})

	_, text, err := executor.Execute(
		ContextWithMaxBudgetUSD(context.Background(), 0.50), nil, "answer")
	if err != nil || text != "exactly affordable" {
		t.Fatalf("exact-budget response=%q err=%v", text, err)
	}

	_, text, err = executor.Execute(context.Background(), nil, "answer again")
	if err != nil || text != "unbudgeted next turn" {
		t.Fatalf("budget leaked: response=%q err=%v", text, err)
	}
}

func TestExecutorBudgetLedgerSurvivesRetriesSharingInvocationContext(t *testing.T) {
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestTextStream("first attempt"),
			buildExecutorTestTextStream("second attempt crosses ceiling"),
		},
	}
	executor := NewExecutor(NewRegistry(), cl, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.60, true
	})
	runCtx := ContextWithMaxBudgetUSD(context.Background(), 1.00)

	_, first, err := executor.Execute(runCtx, nil, "attempt one")
	if err != nil || first != "first attempt" {
		t.Fatalf("first response=%q err=%v", first, err)
	}
	_, _, err = executor.Execute(runCtx, nil, "attempt two")
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("second error = %v, want shared-ledger ErrBudgetExceeded", err)
	}
	var budgetErr *BudgetExceededError
	if !errors.As(err, &budgetErr) || math.Abs(budgetErr.SpentUSD-1.20) > 1e-12 {
		t.Fatalf("shared budget error = %#v", err)
	}
	if cl.next != 2 {
		t.Fatalf("provider calls = %d, want 2", cl.next)
	}
}

func TestExecutorBudgetAllowsTaskToolWithSharedLedger(t *testing.T) {
	registry := NewRegistry()
	taskTool := &scriptedStaticTool{name: "task", content: "delegated"}
	if err := registry.Register(taskTool); err != nil {
		t.Fatal(err)
	}
	cl := &scriptedExecutorClient{
		model: "glm-5.2",
		responses: []*client.StreamingResponse{
			buildExecutorTestStream(client.ResponseChunk{
				FunctionCalls: []*genai.FunctionCall{{
					ID:   "delegation",
					Name: "task",
					Args: map[string]any{"prompt": "work independently"},
				}},
				Done:         true,
				FinishReason: genai.FinishReasonStop,
			}),
			buildExecutorTestTextStream("continued in foreground"),
		},
	}
	executor := NewExecutor(registry, cl, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) {
		return 0.10, true
	})

	_, text, err := executor.Execute(
		ContextWithMaxBudgetUSD(context.Background(), 1.00), nil, "complete safely")
	if err != nil || text != "continued in foreground" {
		t.Fatalf("response=%q err=%v", text, err)
	}
	if taskTool.calls != 1 {
		t.Fatalf("delegated tool calls = %d, want 1", taskTool.calls)
	}
	if len(cl.functionResults) != 1 || len(cl.functionResults[0]) != 1 {
		t.Fatalf("function results = %+v", cl.functionResults)
	}
	if success, _ := cl.functionResults[0][0].Response["success"].(bool); !success {
		t.Fatalf("delegation result = %+v", cl.functionResults[0][0].Response)
	}
}

func assertBudgetHistoryClosesPendingCall(t *testing.T, history []*genai.Content, wantID string) {
	t.Helper()
	if len(history) != 4 {
		t.Fatalf("history length = %d, want 4: %+v", len(history), history)
	}
	if history[1].Role != genai.RoleModel ||
		history[2].Role != genai.RoleUser ||
		history[3].Role != genai.RoleModel {
		t.Fatalf("history roles = %v, %v, %v", history[1].Role, history[2].Role, history[3].Role)
	}
	if len(history[2].Parts) != 1 || history[2].Parts[0].FunctionResponse == nil {
		t.Fatalf("synthetic tool result = %+v", history[2])
	}
	response := history[2].Parts[0].FunctionResponse
	if response.ID != wantID {
		t.Fatalf("synthetic result ID = %q, want %q", response.ID, wantID)
	}
	if success, _ := response.Response["success"].(bool); success {
		t.Fatalf("synthetic result unexpectedly successful: %+v", response.Response)
	}
}
