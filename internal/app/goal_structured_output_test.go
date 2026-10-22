package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"gokin/internal/goals"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func goalStructuredOptions(t *testing.T) HeadlessOptions {
	t.Helper()
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	return HeadlessOptions{OutputFormat: HeadlessOutputJSON, JSONSchema: schema, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 12}
}

func TestGoalEvaluatesValidatedStructuredAnswerOnceWithOriginalToolEvidence(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "notes"}).EnqueueText("The result is ready, but this is not JSON.").EnqueueText(`{"answer":"ready"}`)
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The read output and validated answer demonstrate the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	var stdout bytes.Buffer
	opts := goalStructuredOptions(t)
	opts.Stdout = &stdout
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal inspect notes and return a validated ready answer", opts)
	if err != nil || result.Goal == nil || result.Goal.Status != goals.Achieved || result.Goal.Turns != 1 || result.Goal.NoProgressTurns != 0 || result.StructuredOutput == nil {
		t.Fatalf("structured goal did not finish: %+v %v", result, err)
	}
	if len(worker.Calls()) != 3 || len(evaluator.Calls()) != 1 || worker.Calls()[2].ToolsCount != 0 || !strings.Contains(worker.Calls()[2].Message, "format-only correction") {
		t.Fatal("format correction ran work tools or another goal cycle")
	}
	if !strings.Contains(evaluator.Calls()[0].Message, "ready") || !strings.Contains(evaluator.Calls()[0].Message, "notes inspected") {
		t.Fatal("evaluator missed the final answer or original tool result")
	}
	if result.Goal.InputTokens != result.Usage.InputTokens || result.Goal.OutputTokens != result.Usage.OutputTokens || result.Usage.ModelRounds != 4 {
		t.Fatalf("format work was lost or counted twice: %+v %+v", result.Goal, result.Usage)
	}
	if bytes.Count(stdout.Bytes(), []byte(`"type":"result"`)) != 1 {
		t.Fatal("goal emitted multiple terminal results")
	}
}

func TestStructuredGoalContinuesOnlyAfterItsFormattedVerdict(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText(`{"answer":"first milestone"}`).EnqueueText("The remaining condition is now verified.").EnqueueText(`{"answer":"fully verified"}`)
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"not_met","reason":"Verify the remaining condition with its actual result."}`).EnqueueText(`{"verdict":"met","reason":"The final result demonstrates the entire condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal verify the entire condition", goalStructuredOptions(t))
	if err != nil || result.Goal.Status != goals.Achieved || result.Goal.Turns != 2 || result.Result != `{"answer":"fully verified"}` || len(worker.Calls()) != 3 || len(evaluator.Calls()) != 2 {
		t.Fatalf("formatting disrupted goal continuation: %+v %v", result, err)
	}
	if !strings.Contains(worker.Calls()[1].Message, "Verify the remaining condition") || strings.Contains(worker.Calls()[2].Message, "Independent evaluator") {
		t.Fatal("correction inherited another worker directive")
	}
}

func TestStructuredGoalFailurePausesBeforeAnyCompletionVerdict(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("invalid one").EnqueueText("invalid two").EnqueueText("invalid three")
	evaluator := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal produce the verified JSON answer", goalStructuredOptions(t))
	if err == nil || result.Error == nil || result.Error.Kind != "structured_output" || result.Goal.Status != goals.Active || !result.Goal.Suspended || result.Goal.Turns != 0 || result.StructuredOutput != nil || len(evaluator.Calls()) != 0 || len(worker.Calls()) != 3 {
		t.Fatalf("invalid format achieved or resumed the goal: %+v %v", result, err)
	}
	if result.Goal.InputTokens != result.Usage.InputTokens {
		t.Fatal("failed correction spend was lost")
	}
}

func TestStructuredGoalCorrectionsShareInvocationTurnCap(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("invalid format").EnqueueText(`{"answer":"unreachable"}`)
	evaluator := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	opts := goalStructuredOptions(t)
	opts.MaxTurns = 1
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal return verified JSON", opts)
	if err == nil || result.Error == nil || result.Error.Kind != "max_turns" || len(worker.Calls()) != 1 || len(evaluator.Calls()) != 0 || result.Goal.Status != goals.Active || !result.Goal.Suspended {
		t.Fatalf("correction exceeded the invocation turn limit: %+v %v", result, err)
	}
}

func TestStructuredGoalStatusAndClearNeverCallModel(t *testing.T) {
	for _, prompt := range []string{"/goal", "/goal clear"} {
		t.Run(prompt, func(t *testing.T) {
			worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
			a := newGoalTestApp(t, worker, evaluator)
			if err := a.SetGoal(t.Context(), "preserved condition"); err != nil {
				t.Fatal(err)
			}
			_, _ = a.RunHeadlessWithOptions(t.Context(), prompt, goalStructuredOptions(t))
			if len(worker.Calls()) != 0 || len(evaluator.Calls()) != 0 {
				t.Fatal("schema on a status/clear command launched a model")
			}
		})
	}
}

func TestFormatCorrectionCannotResumePausedGoal(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText(`{"answer":"formatted"}`)
	evaluator := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	if err := a.SetGoal(t.Context(), "original paused condition"); err != nil {
		t.Fatal(err)
	}
	state := a.session.GoalState()
	state.Current.Suspended = true
	state.Current.Reason = "Waiting for explicit user input"
	state.Current.NoProgressTurns = 3
	if err := a.session.SetGoalState(state); err != nil {
		t.Fatal(err)
	}
	before := a.goalIntent.Load()
	ctx := withStructuredOutputCorrection(tools.ContextWithToolCapabilityCeiling(context.Background(), []string{}))
	a.processMessageWithContext(ctx, "format-only correction")
	goal := a.session.GoalState().Current
	if !goal.Suspended || goal.Reason != state.Current.Reason || goal.NoProgressTurns != 3 || len(evaluator.Calls()) != 0 || a.goalIntent.Load() != before {
		t.Fatal("synthetic formatting resumed a paused goal")
	}
}

func TestStructuredGoalUsesOneCorrectionBudgetAcrossAllWorkTurns(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("invalid first").EnqueueText(`{"answer":"first"}`).EnqueueText("invalid second").EnqueueText(`{"answer":"second"}`).EnqueueText("invalid third")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"not_met","reason":"Verify the first remaining requirement."}`).EnqueueText(`{"verdict":"not_met","reason":"Verify the second remaining requirement."}`)
	a := newGoalTestApp(t, worker, evaluator)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal verify all requirements", goalStructuredOptions(t))
	if err == nil || result.Error == nil || result.Error.Kind != "structured_output" || len(worker.Calls()) != 5 || len(evaluator.Calls()) != 2 || result.Goal.Turns != 2 || result.Goal.Status != goals.Active || !result.Goal.Suspended || result.StructuredOutput != nil || result.Goal.InputTokens != result.Usage.InputTokens {
		t.Fatalf("goal reset the invocation correction budget: %+v %v", result, err)
	}
}

func TestStructuredGoalEvaluatorSharesTheRemainingCostBudget(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("invalid format").EnqueueText(`{"answer":"verified"}`)
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The final condition is demonstrated."}`)
	a := newGoalTestApp(t, worker, evaluator)
	a.executor.SetCostCalculator(func(string, string, int, int, int) (float64, bool) { return 0, true })
	opts := goalStructuredOptions(t)
	opts.MaxBudgetUSD = 1e-12
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal return a verified answer", opts)
	if err == nil || result.Error == nil || result.Error.Kind != "budget_exceeded" || result.Goal.Status == goals.Achieved || len(worker.Calls()) != 2 || len(evaluator.Calls()) != 1 || result.Goal.InputTokens != result.Usage.InputTokens {
		t.Fatalf("formatting/evaluation escaped the shared cost budget: %+v %v", result, err)
	}
}

func TestStructuredGoalReplacementStopsOldCorrectionsAndVerdicts(t *testing.T) {
	for _, stage := range []int{1, 2} {
		t.Run(map[int]string{1: "worker", 2: "correction"}[stage], func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("invalid original answer").EnqueueText(`{"answer":"old correction"}`)
			evaluator := testkit.NewMockClient()
			a := newGoalTestApp(t, worker, evaluator)
			worker.OnSend = func(context.Context) {
				if len(worker.Calls()) == stage {
					if err := a.SetGoal(t.Context(), "replacement condition"); err != nil {
						t.Error(err)
					}
				}
			}
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal original condition", goalStructuredOptions(t))
			if err == nil || len(worker.Calls()) != stage || len(evaluator.Calls()) != 0 || result.Goal.Condition != "replacement condition" || result.Goal.Suspended || result.Goal.Status != goals.Active || a.session.GoalState().LastAchieved != nil {
				t.Fatalf("stale formatting modified the replacement: %+v %v", result, err)
			}
		})
	}
}

func TestGoalStructuredDeadlineKeepsTheTimeoutOutcome(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("never delivered")
	evaluator := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	worker.OnSend = func(ctx context.Context) { <-ctx.Done() }
	opts := goalStructuredOptions(t)
	opts.Timeout = 30 * time.Millisecond
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal demonstrate the condition", opts)
	if err == nil || result.Error == nil || result.Error.Kind != "timeout" || result.Status != "timeout" || result.Goal.Status != goals.Active || !result.Goal.Suspended || len(evaluator.Calls()) != 0 {
		t.Fatalf("unfinished goal hid the owning timeout: %+v %v", result, err)
	}
}
