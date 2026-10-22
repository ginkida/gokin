package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/goals"
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

type goalScriptClient struct {
	*testkit.MockClient
	evaluator *goalScriptClient
}

func (*goalScriptClient) GetProvider() string { return "glm" }
func (c *goalScriptClient) CloneForAuxiliaryClient() (client.Client, bool) {
	return c.evaluator, c.evaluator != nil
}

func newGoalTestApp(t *testing.T, worker, evaluator *testkit.MockClient) *App {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	worker.SetModel(config.DefaultGLMModel)
	evaluator.SetModel(config.GLMFlashModel)
	tool := &appHeadlessScriptedTool{name: "read", results: []tools.ToolResult{tools.NewSuccessResult("notes inspected")}}
	a, executor := newHeadlessPolicyTestApp(t, worker, tool)
	base := &goalScriptClient{MockClient: worker, evaluator: &goalScriptClient{MockClient: evaluator}}
	a.client = base
	executor.SetClient(base)
	a.config.Model.Provider = "glm"
	a.config.API.ActiveProvider = "glm"
	a.config.Model.Name = config.DefaultGLMModel
	a.hooksManager = hooks.NewManager(true, a.workDir)
	executor.SetHooks(a.hooksManager)
	a.session.SetWorkDir(a.workDir)
	a.session.SetProvider("glm")
	manager, err := chat.NewSessionManager(a.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatal(err)
	}
	a.sessionManager = manager
	t.Cleanup(func() { _ = a.ReleaseSessionWriterLease() })
	return a
}

func TestHeadlessGoalContinuesUntilIndependentEvaluatorConfirms(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "notes"}).EnqueueText("I inspected notes; a final check remains.").EnqueueText("READY: the requested condition is fully satisfied.")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"not_met","reason":"Verify the remaining condition before claiming success."}`).EnqueueText(`{"verdict":"met","reason":"The read result and final READY response demonstrate the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	var stdout bytes.Buffer
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal inspect notes and reply READY", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard, MaxTurns: 8})
	if err != nil || result.Status != "success" || result.Goal == nil || result.Goal.Status != goals.Achieved || result.Goal.Turns != 2 {
		t.Fatalf("goal did not finish: %+v %v\n%s", result, err, stdout.String())
	}
	if result.Usage.ModelRounds != 5 || result.Usage.InputTokens != 50 {
		t.Fatalf("worker/evaluator usage was not shared: %+v", result.Usage)
	}
	if !strings.HasPrefix(result.Result, "READY:") || strings.Contains(result.Result, "final check remains") {
		t.Fatal("headless result mixed intermediate worker output", result.Result)
	}
	if len(worker.Calls()) != 3 || len(evaluator.Calls()) != 2 {
		t.Fatal("wrong goal work/evaluation count")
	}
	for _, call := range evaluator.Calls() {
		if call.ToolsCount != 0 || len(call.History) != 0 || !strings.Contains(call.Message, "tool_evidence") {
			t.Fatal("evaluator inherited worker tools or missed evidence", call)
		}
	}
	if !strings.Contains(worker.Calls()[2].Message, "Verify the remaining condition") {
		t.Fatal("continuation discarded the independent evaluation reason")
	}
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := history.LoadFull(a.session.GetID())
	if err != nil || saved.Goal.Current == nil || saved.Goal.Current.Status != goals.Achieved {
		t.Fatal("achieved goal was not durable", err)
	}
	if bytes.Count(stdout.Bytes(), []byte(`"type":"result"`)) != 1 {
		t.Fatal("goal emitted multiple terminal JSON objects", stdout.String())
	}
}

func TestGoalStallKeepsConditionAndStopsHeadless(t *testing.T) {
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	for i := 0; i < 3; i++ {
		worker.EnqueueText("I will keep considering the task.")
		evaluator.EnqueueText(`{"verdict":"not_met","reason":"Run the required check instead of restating the plan."}`)
	}
	a := newGoalTestApp(t, worker, evaluator)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal demonstrate the required check", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 10})
	if err == nil || result.Goal == nil || result.Goal.Status != goals.Active || !result.Goal.Suspended || result.Goal.Turns != 3 {
		t.Fatalf("stalled goal was cleared or kept spinning: %+v %v", result, err)
	}
	if len(worker.Calls()) != 3 || len(evaluator.Calls()) != 3 {
		t.Fatal("stall cap did not stop provider requests")
	}
}

func TestGoalEvaluatorRespectsInvocationTurnAndCostLimits(t *testing.T) {
	for _, limit := range []string{"turns", "cost"} {
		t.Run(limit, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Worker response.")
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The condition is demonstrated."}`)
			a := newGoalTestApp(t, worker, evaluator)
			a.executor.SetCostCalculator(func(string, string, int, int, int) (float64, bool) { return 0, true })
			opts := HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 1}
			if limit == "cost" {
				opts.MaxTurns = 4
				opts.MaxBudgetUSD = 1e-12
			}
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal verify the completion condition", opts)
			if err == nil || result.Goal == nil || result.Goal.Status == goals.Achieved {
				t.Fatalf("goal bypassed %s limit: %+v %v", limit, result, err)
			}
			if limit == "turns" && len(evaluator.Calls()) != 0 {
				t.Fatal("evaluator exceeded the invocation turn cap")
			}
			if limit == "cost" && result.Cost.EstimatedUSD <= 0 {
				t.Fatal("evaluator spend was not accounted")
			}
		})
	}
}

func TestStaleGoalVerdictCannotAchieveReplacedOrClearedGoal(t *testing.T) {
	for _, change := range []string{"replace", "clear"} {
		t.Run(change, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Original task completed.")
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"Old condition demonstrated."}`)
			a := newGoalTestApp(t, worker, evaluator)
			evaluator.OnSend = func(context.Context) {
				if change == "replace" {
					if err := a.SetGoal(t.Context(), "a different condition"); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := a.ClearGoal(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, _ = a.RunHeadlessWithOptions(t.Context(), "/goal original condition", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
			state := a.session.GoalState()
			if state.LastAchieved != nil || state.Current.Status == goals.Achieved {
				t.Fatal("stale evaluator achieved a different goal")
			}
		})
	}
}

func TestGoalStatusClearAndDisabledPolicyNeverCallModel(t *testing.T) {
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	a.hooksManager = hooks.NewManager(false, a.workDir)
	if err := a.SetGoal(t.Context(), "do work"); err == nil {
		t.Fatal("disabled hook policy allowed an evaluator")
	}
	for _, prompt := range []string{"/goal", "/goal clear"} {
		result, err := a.RunHeadlessWithOptions(t.Context(), prompt, HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
		if err != nil || !strings.Contains(result.Result, "No goal set") {
			t.Fatal(result, err)
		}
	}
	if len(worker.Calls()) != 0 || len(evaluator.Calls()) != 0 {
		t.Fatal("status/clear invoked a provider")
	}
}

func TestGoalUserQueueWinsAndClearDropsOnlyAutomaticWork(t *testing.T) {
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	if err := a.SetGoal(t.Context(), "required condition"); err != nil {
		t.Fatal(err)
	}
	goal := a.session.GoalState().Current
	auto := &goalContinuation{goalID: goal.ID, sessionID: a.session.GetID(), prompt: "automatic work"}
	a.replaceQueuedGoal(auto)
	a.enqueuePending("user correction")
	a.replaceQueuedGoal(auto)
	request, _, ok := a.dequeuePendingRequest()
	if !ok || request.message != "user correction" || a.pendingCount() != 0 {
		t.Fatal("automatic work overtook explicit user input")
	}
	a.replaceQueuedGoal(auto)
	a.enqueuePending("keep user input")
	if _, err := a.ClearGoal(t.Context()); err != nil {
		t.Fatal(err)
	}
	request, _, ok = a.dequeuePendingRequest()
	if !ok || request.message != "keep user input" || a.pendingCount() != 0 {
		t.Fatal("goal clear removed user input or left an automatic actor")
	}
}

func TestPlanContextPreservesGoalButClearAndPreviousResumeDoNotRearmIt(t *testing.T) {
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	if err := a.SetGoal(t.Context(), "complete the entire plan"); err != nil {
		t.Fatal(err)
	}
	id := a.session.GoalState().Current.ID
	a.session.AddUserMessage("original task")
	if _, err := a.replacePlanConversationPersistenceBoundary(func() { a.session.AddUserMessage("approved plan") }); err != nil {
		t.Fatal(err)
	}
	if goal := a.session.GoalState().Current; goal == nil || goal.ID != id || goal.Status != goals.Active {
		t.Fatal("plan compaction cleared the active completion condition")
	}
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.session.GoalState().Current != nil {
		t.Fatal("clear kept an active goal")
	}
	if _, err := a.ResumePreviousClearSession(t.Context(), a.PreviousClearSessionID(), false); err != nil {
		t.Fatal(err)
	}
	if goal := a.session.GoalState().Current; goal != nil && goal.Status == goals.Active {
		t.Fatal("previous-dialog resume rearmed a deliberately cleared goal")
	}
}

func TestRevokedGoalPolicyStopsQueuedWorkBeforeModelRequest(t *testing.T) {
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	if err := a.SetGoal(t.Context(), "finish condition"); err != nil {
		t.Fatal(err)
	}
	goal := a.session.GoalState().Current
	actor := &goalContinuation{sessionID: a.session.GetID(), goalID: goal.ID, epoch: a.recoveryEpoch.Load(), turns: goal.Turns}
	a.hooksManager = hooks.NewManager(false, a.workDir)
	a.processMessageWithContext(context.WithValue(t.Context(), goalContinuationKey{}, actor), "continue")
	if len(worker.Calls()) != 0 || len(evaluator.Calls()) != 0 || !a.session.GoalState().Current.Suspended {
		t.Fatal("revoked policy allowed another automatic model turn")
	}
}

func TestGoalEvaluatorCannotHideUnpricedFallbackSpend(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Work reported complete.")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"Condition demonstrated."}`)
	a := newGoalTestApp(t, worker, evaluator)
	a.executor.SetCostCalculator(func(string, string, int, int, int) (float64, bool) { return 0, true })
	// Simulate a fallback whose active identity changes during the request.
	evaluator.OnSend = func(context.Context) { evaluator.SetModel("unknown-active-model") }
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal verify the whole condition", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4, MaxBudgetUSD: 1})
	if err == nil || result.Error == nil || result.Error.Kind != "cost_unavailable" || result.Goal.Status == goals.Achieved || result.Cost.Tracked {
		t.Fatalf("unpriced evaluator fallback reported success: %+v %v", result, err)
	}
}
