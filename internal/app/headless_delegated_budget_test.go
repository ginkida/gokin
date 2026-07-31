package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

type sharedBudgetTestClient struct {
	*testkit.MockClient
}

func (c *sharedBudgetTestClient) GetProvider() string { return "glm" }
func (c *sharedBudgetTestClient) WithModel(string) client.Client {
	return c
}

func TestHeadlessBudgetIsSharedAcrossForegroundTaskAndChildAgent(t *testing.T) {
	model := &sharedBudgetTestClient{MockClient: testkit.NewMockClient()}
	model.SetModel("glm-5.2")
	model.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID:   "task-1",
			Name: "task",
			Args: map[string]any{
				"prompt":        "perform the delegated change",
				"subagent_type": "general",
				"max_turns":     2,
			},
		}}},
		// $0.40 at the maintained GLM-5.2 output rate.
		{Done: true, OutputTokens: 25_000},
	}})
	model.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID: "write-1", Name: "write", Args: map[string]any{"path": "x"},
		}}},
		// $0.60: the shared ledger reaches the exact $1 ceiling. Because this
		// response requests a tool, the child must pair but not execute it.
		{Done: true, OutputTokens: 37_500},
	}})

	mutation := &appHeadlessScriptedTool{
		name:    "write",
		results: []tools.ToolResult{tools.NewSuccessResult("must not run")},
	}
	task := tools.NewTaskTool()
	registry := tools.NewRegistry()
	if err := registry.Register(mutation); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(task); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	workDir := t.TempDir()
	runner := agent.NewRunner(ctx, model, registry, workDir)
	types := agent.NewAgentTypeRegistry()
	runner.SetTypeRegistry(types)
	task.SetRunner(&agentRunnerAdapter{runner: runner})
	task.SetAgentTypeProvider(types)

	executor := tools.NewExecutor(registry, model, time.Second)
	executor.SetCostCalculator(func(_, _ string, _, output, _ int) (float64, bool) {
		return float64(output) * 16 / 1_000_000, true
	})
	cfg := config.DefaultConfig()
	cfg.Model.Provider = "glm"
	cfg.Model.Name = "glm-5.2"
	cfg.DoneGate.Enabled = false
	application := &App{
		config:              cfg,
		workDir:             workDir,
		client:              model,
		registry:            registry,
		executor:            executor,
		agentRunner:         runner,
		session:             chat.NewSession(),
		ctx:                 ctx,
		cancel:              cancel,
		rateLimitRetryCount: make(map[string]int),
	}
	executor.SetHandler(application.buildExecutionHandler(nil))

	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(
		context.Background(),
		"delegate the change",
		HeadlessOptions{
			OutputFormat: HeadlessOutputJSON,
			Stdout:       &stdout,
			Stderr:       io.Discard,
			MaxBudgetUSD: 1,
		},
	)
	if !errors.Is(err, tools.ErrBudgetExceeded) {
		t.Fatalf("RunHeadlessWithOptions() error = %v, want ErrBudgetExceeded", err)
	}
	if result.Error == nil || result.Error.Kind != "budget_exceeded" {
		t.Fatalf("terminal result = %+v", result)
	}
	if mutation.CallCount() != 0 {
		t.Fatalf("delegated mutation ran %d times", mutation.CallCount())
	}
	if !result.Cost.Tracked || result.Cost.EstimatedUSD != 1 {
		t.Fatalf("authoritative shared cost = %+v, want tracked $1", result.Cost)
	}
	if calls := model.Calls(); len(calls) != 2 {
		t.Fatalf("provider calls = %d, want foreground + child only", len(calls))
	}
	decoded := decodeSingleHeadlessResult(t, stdout.Bytes())
	if decoded.Error == nil || decoded.Error.Kind != "budget_exceeded" ||
		decoded.Cost.EstimatedUSD != 1 {
		t.Fatalf("encoded terminal result = %+v", decoded)
	}
}
