package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/tools"

	"google.golang.org/genai"
)

func fixedInvocationCost(cost float64) invocationCostCalculator {
	return func(string, string, int, int, int) (float64, bool) {
		return cost, true
	}
}

func TestInvocationBudgetClientExactBudgetRejectsPendingTools(t *testing.T) {
	base := testkit.NewMockClient()
	base.SetModel("glm-5.2")
	base.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID: "write-1", Name: "write", Args: map[string]any{"path": "x"},
		}}},
		{Done: true, InputTokens: 10, OutputTokens: 5},
	}})
	metered := newInvocationBudgetClientWithCalculator(base, fixedInvocationCost(1))
	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 1)

	stream, err := metered.SendMessage(ctx, "change a file")
	if err != nil {
		t.Fatalf("SendMessage() startup error = %v", err)
	}
	resp, err := stream.Collect()
	if !errors.Is(err, tools.ErrBudgetExceeded) {
		t.Fatalf("Collect() error = %v, want ErrBudgetExceeded", err)
	}
	if resp == nil || len(resp.FunctionCalls) != 1 || resp.FunctionCalls[0].ID != "write-1" {
		t.Fatalf("partial response lost pending tool call: %+v", resp)
	}
	ledger, _ := tools.InvocationBudgetLedgerFromContext(ctx)
	limit, spent := ledger.Snapshot()
	if limit != 1 || spent != 1 {
		t.Fatalf("ledger = limit %v spent %v, want 1/1", limit, spent)
	}
}

func TestInvocationBudgetClientAllowsFinalTextAtExactBudget(t *testing.T) {
	base := testkit.NewMockClient()
	base.SetModel("glm-5.2")
	base.EnqueueText("complete")
	metered := newInvocationBudgetClientWithCalculator(base, fixedInvocationCost(1))
	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 1)

	stream, err := metered.SendMessage(ctx, "answer")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Collect()
	if err != nil {
		t.Fatalf("final text at exact budget failed: %v", err)
	}
	if resp.Text != "complete" {
		t.Fatalf("response text = %q", resp.Text)
	}
}

func TestInvocationBudgetClientFailsClosedBeforeUnknownPriceRequest(t *testing.T) {
	base := testkit.NewMockClient()
	base.SetModel("private-model")
	metered := newInvocationBudgetClientWithCalculator(
		base,
		func(string, string, int, int, int) (float64, bool) { return 0, false },
	)
	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 1)

	if _, err := metered.SendMessage(ctx, "must not send"); !errors.Is(err, tools.ErrCostUnavailable) {
		t.Fatalf("SendMessage() error = %v, want ErrCostUnavailable", err)
	}
	if calls := base.Calls(); len(calls) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(calls))
	}
}

func TestInvocationBudgetClientSerializesSiblingProviderRounds(t *testing.T) {
	base := testkit.NewMockClient()
	base.SetModel("glm-5.2")
	base.EnqueueText("one")
	base.EnqueueText("two")

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var sends atomic.Int32
	base.OnSend = func(context.Context) {
		switch sends.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			close(secondStarted)
		}
	}

	metered := newInvocationBudgetClientWithCalculator(base, fixedInvocationCost(0.4))
	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 1)
	errs := make(chan error, 2)
	run := func(message string) {
		stream, err := metered.SendMessage(ctx, message)
		if err == nil {
			_, err = stream.Collect()
		}
		errs <- err
	}

	go run("first")
	<-firstStarted
	go run("second")

	select {
	case <-secondStarted:
		t.Fatal("second provider request started while first held the budget lease")
	case <-time.After(40 * time.Millisecond):
	}
	close(releaseFirst)

	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second provider request did not start after first round completed")
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("metered request failed: %v", err)
		}
	}
	ledger, _ := tools.InvocationBudgetLedgerFromContext(ctx)
	_, spent := ledger.Snapshot()
	if spent != 0.8 {
		t.Fatalf("shared spend = %v, want 0.8", spent)
	}
}

type budgetMutationProbeTool struct {
	calls atomic.Int32
}

func (t *budgetMutationProbeTool) Name() string        { return "write" }
func (t *budgetMutationProbeTool) Description() string { return "mutation probe" }
func (t *budgetMutationProbeTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: t.Name()}
}
func (t *budgetMutationProbeTool) Validate(map[string]any) error { return nil }
func (t *budgetMutationProbeTool) Execute(context.Context, map[string]any) (tools.ToolResult, error) {
	t.calls.Add(1)
	return tools.NewSuccessResult("mutated"), nil
}

func TestAgentBudgetFailurePairsToolCallWithoutExecutingIt(t *testing.T) {
	base := testkit.NewMockClient()
	base.SetModel("glm-5.2")
	base.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID: "write-1", Name: "write", Args: map[string]any{"path": "x"},
		}}},
		{Done: true, InputTokens: 10, OutputTokens: 5},
	}})
	metered := newInvocationBudgetClientWithCalculator(base, fixedInvocationCost(1))
	probe := &budgetMutationProbeTool{}
	registry := tools.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}

	a := NewAgent(AgentTypeGeneral, nil, registry, t.TempDir(), 2, "", nil, nil)
	a.client = metered
	result, err := a.Run(
		tools.ContextWithMaxBudgetUSD(context.Background(), 1),
		"change a file",
	)
	if !errors.Is(err, tools.ErrBudgetExceeded) {
		t.Fatalf("Run() error = %v, want ErrBudgetExceeded", err)
	}
	if probe.calls.Load() != 0 {
		t.Fatalf("mutation tool executed %d times", probe.calls.Load())
	}
	if result == nil || result.Status != AgentStatusFailed {
		t.Fatalf("result = %+v", result)
	}

	a.stateMu.RLock()
	history := append([]*genai.Content(nil), a.history...)
	a.stateMu.RUnlock()
	if len(history) < 3 {
		t.Fatalf("history too short: %+v", history)
	}
	modelCall := history[len(history)-3]
	toolResult := history[len(history)-2]
	terminal := history[len(history)-1]
	if modelCall.Role != genai.RoleModel || toolResult.Role != genai.RoleUser ||
		terminal.Role != genai.RoleModel {
		t.Fatalf("terminal history roles = %v/%v/%v",
			modelCall.Role, toolResult.Role, terminal.Role)
	}
	if len(toolResult.Parts) != 1 || toolResult.Parts[0].FunctionResponse == nil ||
		toolResult.Parts[0].FunctionResponse.ID != "write-1" {
		t.Fatalf("synthetic tool result = %+v", toolResult)
	}
}

type budgetRunnerClient struct {
	*testkit.MockClient
}

type auxiliaryCloneProbeClient struct {
	*testkit.MockClient
	lastClone *auxiliaryCloneProbeClient
}

func (c *auxiliaryCloneProbeClient) CloneForAuxiliaryClient() (client.Client, bool) {
	clone := &auxiliaryCloneProbeClient{MockClient: testkit.NewMockClient()}
	clone.SetModel(c.GetModel())
	clone.SetTools(c.GetTools())
	clone.SetSystemInstruction(c.SystemInstruction())
	clone.SetTurnContext(c.TurnContext())
	clone.SetThinkingBudget(c.ThinkingBudget())
	c.lastClone = clone
	return clone, true
}

func TestInvocationBudgetClientAuxiliaryCloneIsIsolatedAndMetered(t *testing.T) {
	base := &auxiliaryCloneProbeClient{MockClient: testkit.NewMockClient()}
	base.SetModel("glm-5.2")
	base.SetTools([]*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "write"}}}})
	base.SetSystemInstruction("foreground system")
	base.SetTurnContext("foreground turn")
	base.SetThinkingBudget(8192)
	metered := newInvocationBudgetClientWithCalculator(base, fixedInvocationCost(1))

	got, isolated := client.CloneForAuxiliary(metered)
	if !isolated {
		t.Fatal("metered wrapper was not isolated")
	}
	aux, ok := got.(*invocationBudgetClient)
	if !ok || aux == metered {
		t.Fatalf("auxiliary client = %T %p, want distinct invocationBudgetClient", got, got)
	}
	clone := base.lastClone
	if clone == nil || aux.base != clone {
		t.Fatalf("auxiliary base = %T, clone = %p", aux.base, clone)
	}
	if len(clone.GetTools()) != 0 || clone.SystemInstruction() != "" ||
		clone.TurnContext() != "" || clone.ThinkingBudget() != 0 {
		t.Fatalf("auxiliary state leaked: tools=%d system=%q turn=%q thinking=%d",
			len(clone.GetTools()), clone.SystemInstruction(), clone.TurnContext(), clone.ThinkingBudget())
	}
	if len(base.GetTools()) != 1 || base.SystemInstruction() == "" ||
		base.TurnContext() == "" || base.ThinkingBudget() != 8192 {
		t.Fatal("foreground base was mutated while cloning auxiliary client")
	}

	clone.EnqueueText("semantic result")
	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 2)
	stream, err := aux.SendMessage(ctx, "reflect")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatal(err)
	}
	ledger, _ := tools.InvocationBudgetLedgerFromContext(ctx)
	limit, spent := ledger.Snapshot()
	if limit != 2 || spent != 1 {
		t.Fatalf("auxiliary budget = limit %v spent %v, want 2/1", limit, spent)
	}

	reflector := NewReflector()
	reflector.SetClient(metered)
	if reflector.client == metered {
		t.Fatal("reflector retained the foreground metered wrapper")
	}
	if _, ok := reflector.client.(*invocationBudgetClient); !ok {
		t.Fatalf("reflector client = %T, want invocationBudgetClient", reflector.client)
	}
}

func (c *budgetRunnerClient) GetProvider() string { return "glm" }
func (c *budgetRunnerClient) WithModel(string) client.Client {
	return c
}

type budgetTaskRunnerAdapter struct {
	runner *Runner
}

func (a budgetTaskRunnerAdapter) Spawn(
	ctx context.Context, agentType, prompt string, maxTurns int, model string,
) (string, error) {
	return a.runner.Spawn(ctx, agentType, prompt, maxTurns, model)
}
func (a budgetTaskRunnerAdapter) SpawnAsync(
	ctx context.Context, agentType, prompt string, maxTurns int, model string,
) string {
	return a.runner.SpawnAsync(ctx, agentType, prompt, maxTurns, model)
}
func (a budgetTaskRunnerAdapter) SpawnAsyncWithStreaming(
	ctx context.Context,
	agentType, prompt string,
	maxTurns int,
	model string,
	onText func(string),
	_ func(string, *tools.AgentProgress),
) string {
	return a.runner.SpawnAsyncWithStreaming(
		ctx, agentType, prompt, maxTurns, model, onText, nil)
}
func (a budgetTaskRunnerAdapter) Resume(
	ctx context.Context, agentID, prompt string,
) (string, error) {
	return a.runner.Resume(ctx, agentID, prompt)
}
func (a budgetTaskRunnerAdapter) ResumeAsync(
	ctx context.Context, agentID, prompt string,
) (string, error) {
	return a.runner.ResumeAsync(ctx, agentID, prompt)
}
func (a budgetTaskRunnerAdapter) GetResult(agentID string) (tools.AgentResult, bool) {
	result, ok := a.runner.GetResult(agentID)
	if !ok || result == nil {
		return tools.AgentResult{}, false
	}
	return tools.AgentResult{
		AgentID:       result.AgentID,
		Type:          string(result.Type),
		Model:         result.Model,
		Provider:      result.Provider,
		EstimatedCost: result.EstimatedCost,
		CostTracked:   result.CostTracked,
		Status:        string(result.Status),
		Output:        result.Output,
		Error:         result.Error,
		Duration:      result.Duration,
		Completed:     result.Completed,
		OutputFile:    result.OutputFile,
		PolicyBlock:   result.PolicyBlock,
	}, true
}

func TestTaskToolAndRunnerInheritOneInvocationBudget(t *testing.T) {
	base := &budgetRunnerClient{MockClient: testkit.NewMockClient()}
	base.SetModel("glm-5.2")
	// GLM-5.2 output costs $16 / 1M tokens, so 62,500 output tokens
	// consume the exact $1 test budget.
	base.EnqueueScript(testkit.ResponseScript{Chunks: []client.ResponseChunk{
		{FunctionCalls: []*genai.FunctionCall{{
			ID: "write-1", Name: "write", Args: map[string]any{"path": "x"},
		}}},
		{Done: true, InputTokens: 0, OutputTokens: 62_500},
	}})

	probe := &budgetMutationProbeTool{}
	registry := tools.NewRegistry()
	if err := registry.Register(probe); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(context.Background(), base, registry, t.TempDir())
	typeRegistry := NewAgentTypeRegistry()
	runner.SetTypeRegistry(typeRegistry)

	task := tools.NewTaskTool()
	task.SetRunner(budgetTaskRunnerAdapter{runner: runner})
	task.SetAgentTypeProvider(typeRegistry)
	task.SetBackgroundAllowed(false)
	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 1)
	result, err := task.Execute(ctx, map[string]any{
		"prompt":        "change a file",
		"subagent_type": "general",
		"max_turns":     2,
	})
	if err != nil {
		t.Fatalf("task.Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("task result = %+v", result)
	}
	if probe.calls.Load() != 0 {
		t.Fatalf("delegated mutation executed %d times", probe.calls.Load())
	}
	if !strings.Contains(result.Content, "maximum cost budget reached") {
		t.Fatalf("task result did not preserve typed child failure: %q", result.Content)
	}
	ledger, _ := tools.InvocationBudgetLedgerFromContext(ctx)
	limit, spent := ledger.Snapshot()
	if limit != 1 || spent != 1 {
		t.Fatalf("shared ledger = limit %v spent %v, want 1/1", limit, spent)
	}
	if calls := base.Calls(); len(calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(calls))
	}
}

func TestRunnerBindsPlannerAndReflectorToMeteredAgentClient(t *testing.T) {
	base := &budgetRunnerClient{MockClient: testkit.NewMockClient()}
	base.SetModel("glm-5.2")
	runner := NewRunner(context.Background(), base, tools.NewRegistry(), t.TempDir())
	runner.SetTreePlanner(NewTreePlanner(
		DefaultTreePlannerConfig(), nil, NewReflector(), base))

	ctx := tools.ContextWithMaxBudgetUSD(context.Background(), 1)
	configured := runner.newConfiguredAgent(
		ctx, runner.snapshotAgentDeps(), "general", 2, "", nil)
	metered, ok := configured.client.(*invocationBudgetClient)
	if !ok || metered == nil {
		t.Fatalf("configured client = %T, want invocationBudgetClient", configured.client)
	}
	if configured.treePlanner == nil || configured.treePlanner.client != configured.client {
		t.Fatalf("planner client = %T, agent client = %T",
			configured.treePlanner.client, configured.client)
	}
	if configured.treePlanner.reflector != configured.reflector {
		t.Fatal("planner retained prototype reflector instead of metered agent reflector")
	}
}
