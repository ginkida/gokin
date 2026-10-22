package app

import (
	"context"
	"io"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/goals"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

type goalAuxiliaryRead struct{ model client.Client }

func (goalAuxiliaryRead) Name() string        { return "read" }
func (goalAuxiliaryRead) Description() string { return "inspect proof with a model summary" }
func (goalAuxiliaryRead) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "read"}
}
func (goalAuxiliaryRead) Validate(map[string]any) error { return nil }
func (tool goalAuxiliaryRead) Execute(ctx context.Context, _ map[string]any) (tools.ToolResult, error) {
	_, err := appcontext.NewSummarizer(tool.model).Summarize(ctx, []*genai.Content{genai.NewContentFromText("acceptance proof", genai.RoleUser)})
	if err != nil {
		return tools.NewErrorResult(err.Error()), nil
	}
	return tools.NewSuccessResult("Summarized and inspected the acceptance proof"), nil
}

func TestGoalAuxiliaryRequestsShareInvocationUsageAndLimits(t *testing.T) {
	for _, cap := range []int{4, 2, 1} {
		t.Run(map[int]string{4: "complete", 2: "after-summary", 1: "before-summary"}[cap], func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"}).EnqueueText("The summarized proof demonstrates the full condition")
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The inspected proof demonstrates the whole condition."}`)
			a := newGoalTestApp(t, worker, evaluator)
			auxiliary := testkit.NewMockClient().EnqueueText("The acceptance proof and its result are preserved in this summary.")
			auxiliary.SetModel(config.GLMFlashModel)
			a.registry.Unregister("read")
			a.registry.MustRegister(goalAuxiliaryRead{model: auxiliary})
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal inspect and summarize the full proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: cap})
			if result.Goal.InputTokens != cap*10 || result.Usage.InputTokens != cap*10 || result.Usage.ModelRounds != cap {
				t.Fatalf("auxiliary usage was lost or doubled: %+v err=%v", result, err)
			}
			if cap == 4 {
				if err != nil || result.Goal.Status != goals.Achieved || len(auxiliary.Calls()) != 1 {
					t.Fatalf("summary did not reach evaluation: %+v %v", result, err)
				}
			} else if err == nil || result.Error.Kind != "max_turns" || result.Goal.Status != goals.Active || !result.Goal.Suspended || len(evaluator.Calls()) != 0 || len(auxiliary.Calls()) != cap-1 {
				t.Fatalf("summary bypassed the shared cap: %+v err=%v auxiliary=%d", result, err, len(auxiliary.Calls()))
			}
		})
	}
}

func TestGoalAuxiliaryRequestConsumesTheSharedCostBudget(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"}).EnqueueText("must not reach provider")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	a.executor.SetCostCalculator(func(string, string, int, int, int) (float64, bool) { return 0, true })
	auxiliary := testkit.NewMockClient().EnqueueText("A real billed summary consumes the remaining cost allowance.")
	auxiliary.SetModel(config.GLMFlashModel)
	a.registry.Unregister("read")
	a.registry.MustRegister(goalAuxiliaryRead{model: auxiliary})
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal inspect the complete proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 8, MaxBudgetUSD: 1e-12})
	if err == nil || result.Error.Kind != "budget_exceeded" || len(worker.Calls()) != 1 || len(auxiliary.Calls()) != 1 || result.Goal.InputTokens != 20 || result.Usage.InputTokens != 20 || result.Cost.EstimatedUSD <= 1e-12 {
		t.Fatalf("auxiliary cost escaped the invocation: %+v %v", result, err)
	}
}
