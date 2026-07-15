package app

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/plan"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func TestExecuteDirectStepUsageAggregatesRetryAndSuccess(t *testing.T) {
	mock := testkit.NewMockClient()
	mock.EnqueueScript(directPlanUsageErrorScript(
		errors.New("request timeout"), 7, 2, 3))
	mock.EnqueueScript(directPlanUsageSuccessScript(
		"Implemented the step and tests pass.", 11, 5, 4))

	a, approvedPlan, step := newDirectPlanUsageHarness(t, mock, true)
	a.executeDirectStep(
		context.Background(), step, approvedPlan, 1, nil, 2,
		[]time.Duration{0},
	)

	if got := len(mock.Calls()); got != 2 {
		t.Fatalf("model calls = %d, want 2 (retry + success)", got)
	}
	persisted := approvedPlan.GetStep(step.ID)
	if persisted.Status != plan.StatusCompleted {
		t.Fatalf("step status = %s, want completed (error=%q)", persisted.Status, persisted.Error)
	}
	assertDirectPlanUsage(t, a, persisted, 18, 7, 7)
	if diff := math.Abs(a.totalEstimatedCost - 0.025); diff > 1e-12 {
		t.Fatalf("totalEstimatedCost = %.12f, want 0.025", a.totalEstimatedCost)
	}
	if !a.costTracked {
		t.Fatal("costTracked = false, want true for two priced attempts")
	}
}

func TestExecuteDirectStepUsageCommittedBeforeVerificationPause(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueScript(
		directPlanUsageSuccessScript("Implementation finished.", 13, 4, 2))

	a, approvedPlan, step := newDirectPlanUsageHarness(t, mock, false)
	a.executeDirectStep(
		context.Background(), step, approvedPlan, 1, nil, 1, nil,
	)

	persisted := approvedPlan.GetStep(step.ID)
	if persisted.Status != plan.StatusPaused {
		t.Fatalf("step status = %s, want paused by unavailable verification (error=%q)",
			persisted.Status, persisted.Error)
	}
	assertDirectPlanUsage(t, a, persisted, 13, 4, 2)
}

func TestExecuteDirectStepUsageCommittedOnFailedStep(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueScript(
		directPlanUsageErrorScript(errors.New("permission denied by provider"), 19, 6, 5))

	a, approvedPlan, step := newDirectPlanUsageHarness(t, mock, false)
	a.executeDirectStep(
		context.Background(), step, approvedPlan, 1, nil, 3,
		[]time.Duration{0, 0},
	)

	if got := len(mock.Calls()); got != 1 {
		t.Fatalf("model calls = %d, want 1 for fatal failure", got)
	}
	persisted := approvedPlan.GetStep(step.ID)
	if persisted.Status != plan.StatusFailed {
		t.Fatalf("step status = %s, want failed (error=%q)", persisted.Status, persisted.Error)
	}
	assertDirectPlanUsage(t, a, persisted, 19, 6, 5)
}

func newDirectPlanUsageHarness(
	t *testing.T,
	mock *testkit.MockClient,
	withVerifier bool,
) (*App, *plan.Plan, *plan.Step) {
	t.Helper()

	registry := tools.NewRegistry()
	if withVerifier {
		if err := registry.Register(directPlanVerifierTool{}); err != nil {
			t.Fatalf("register verifier: %v", err)
		}
	}
	executor := tools.NewExecutor(registry, mock, time.Second)
	executor.SetCostCalculator(func(_ string, _ string, input, output, _ int) (float64, bool) {
		return float64(input+output) / 1000, true
	})

	cfg := config.DefaultConfig()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.Plan.DefaultStepTimeout = time.Second
	cfg.Permission.Enabled = false

	manager := plan.NewManager(true, false)
	approvedPlan := manager.CreatePlan("usage contract", "", "execute one step")
	approvedPlan.AddStep("implement", "implement and verify the requested change")
	step := approvedPlan.GetStep(1)
	if step == nil {
		t.Fatal("plan step is nil")
	}

	a := &App{
		config:      cfg,
		workDir:     t.TempDir(),
		client:      mock,
		registry:    registry,
		executor:    executor,
		session:     chat.NewSession(),
		planManager: manager,
		ctx:         context.Background(),
	}
	return a, approvedPlan, step
}

func directPlanUsageSuccessScript(text string, input, output, cacheRead int) testkit.ResponseScript {
	return testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{Text: text},
		{
			Done:                 true,
			FinishReason:         genai.FinishReasonStop,
			InputTokens:          input,
			OutputTokens:         output,
			CacheReadInputTokens: cacheRead,
		},
	}}
}

func directPlanUsageErrorScript(err error, input, output, cacheRead int) testkit.ResponseScript {
	return testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{
			Text:                 "partial response",
			InputTokens:          input,
			OutputTokens:         output,
			CacheReadInputTokens: cacheRead,
		},
		{Error: err, Done: true},
	}}
}

func assertDirectPlanUsage(
	t *testing.T,
	a *App,
	step *plan.Step,
	wantInput, wantOutput, wantCache int,
) {
	t.Helper()

	if got, want := step.TokensUsed, wantInput+wantOutput; got != want {
		t.Errorf("step TokensUsed = %d, want %d", got, want)
	}
	if a.totalInputTokens != wantInput {
		t.Errorf("totalInputTokens = %d, want %d", a.totalInputTokens, wantInput)
	}
	if a.totalOutputTokens != wantOutput {
		t.Errorf("totalOutputTokens = %d, want %d", a.totalOutputTokens, wantOutput)
	}
	if a.totalCacheReadTokens != wantCache {
		t.Errorf("totalCacheReadTokens = %d, want %d", a.totalCacheReadTokens, wantCache)
	}
}

type directPlanVerifierTool struct{}

func (directPlanVerifierTool) Name() string        { return "bash" }
func (directPlanVerifierTool) Description() string { return "test-only verifier" }
func (directPlanVerifierTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "bash", Description: "test-only verifier"}
}
func (directPlanVerifierTool) Validate(map[string]any) error { return nil }
func (directPlanVerifierTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	return tools.NewSuccessResult("verification passed"), nil
}
