package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/config"
	appcontext "gokin/internal/context"
	"gokin/internal/goals"
	"gokin/internal/plan"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func configureGoalPlan(t *testing.T, a *App, worker *testkit.MockClient) (*plan.Plan, *plan.Step) {
	t.Helper()
	a.config.Plan.ClearContext = true
	a.config.Plan.DelegateSteps = false
	a.config.Plan.DefaultStepTimeout = 2 * time.Second
	a.promptBuilder = appcontext.NewPromptBuilder(a.workDir, nil)
	a.planManager = plan.NewManager(true, false)
	p := a.planManager.CreatePlan("Verified approved plan", "complete the approved step", "complete the whole requested condition")
	p.AddStep("inspect and verify", "inspect the requested result and verify the check")
	step := p.GetStep(1)
	step.VerifyCommands = []string{"go test ./..."}
	step.SuccessCriteria = []string{"verification succeeds"}
	if err := p.TransitionLifecycle(plan.LifecycleAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.Register(directPlanVerifierTool{}); err != nil {
		t.Fatal(err)
	}
	worker.OnSend = func(context.Context) {
		if len(worker.Calls()) == 1 {
			if err := a.planManager.ApprovePlanAndRequestContextClearIfCurrent(p); err != nil {
				t.Error(err)
			}
		}
	}
	return p, step
}

func configureGoalPlanRunner(t *testing.T, a *App, child *testkit.MockClient) *agent.Runner {
	t.Helper()
	child.SetModel(config.DefaultGLMModel)
	runner := agent.NewRunner(a.ctx, &sharedBudgetTestClient{MockClient: child}, a.registry, a.workDir)
	runner.SetOnAgentUsage(func(_ string, result *agent.AgentResult) { a.accumulateScopedAgentResultUsage(result) })
	runner.SetOnSubAgentActivity(a.handleSubAgentActivity)
	a.agentRunner = runner
	return runner
}

func TestGoalApprovedPlanReachesIndependentEvaluationWithAllOwnedUsage(t *testing.T) {
	for _, mode := range []string{"direct", "delegated", "nested"} {
		t.Run(mode, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Approved plan is ready")
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The approved step and native verification output demonstrate the entire condition."}`)
			a := newGoalTestApp(t, worker, evaluator)
			p, _ := configureGoalPlan(t, a, worker)
			child := testkit.NewMockClient().EnqueueText("The delegated result was inspected and verified")
			wantInput := 30
			if mode == "direct" {
				worker.EnqueueText("The direct step result was inspected and verified")
			}
			if mode == "delegated" {
				configureGoalPlanRunner(t, a, child)
				a.config.Plan.DelegateSteps = true
			}
			if mode == "nested" {
				runner := configureGoalPlanRunner(t, a, child)
				task := tools.NewTaskTool()
				task.SetRunner(&agentRunnerAdapter{runner: runner})
				task.SetAgentTypeProvider(agent.NewAgentTypeRegistry())
				if err := a.registry.Register(task); err != nil {
					t.Fatal(err)
				}
				worker.EnqueueToolCall("task", map[string]any{"subagent_type": "general", "prompt": "inspect the nested result", "max_turns": 2}).EnqueueText("Nested task result was inspected and verified")
				wantInput = 50
			}
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal execute and verify the entire approved plan", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 12})
			if err != nil || result.Goal == nil || result.Goal.Status != goals.Achieved || result.Goal.Turns != 1 || p.GetStatus() != plan.StatusCompleted {
				t.Fatalf("plan bypassed goal completion: %+v %v, plan=%s", result, err, p.GetStatus())
			}
			if len(evaluator.Calls()) != 1 || !strings.Contains(evaluator.Calls()[0].Message, "verification passed") || !strings.Contains(evaluator.Calls()[0].Message, "Plan step evidence") {
				t.Fatal("independent evaluator did not receive native plan evidence")
			}
			if result.Goal.InputTokens != wantInput || result.Usage.InputTokens != wantInput || result.Goal.OutputTokens != result.Usage.OutputTokens {
				t.Fatalf("plan usage was lost, mixed or counted twice: goal=%+v invocation=%+v", result.Goal, result.Usage)
			}
			if result.Usage.ModelRounds != len(worker.Calls())+len(child.Calls())+len(evaluator.Calls()) {
				t.Fatal("invocation omitted a nested model round", result.Usage)
			}
			if !strings.Contains(result.Result, "Plan Execution Summary") {
				t.Fatal("headless result lost the plan's terminal answer")
			}
			if a.recoveryEpoch.Load() == 0 {
				t.Fatal("test did not exercise the plan context replacement")
			}
		})
	}
}

func TestGoalContinuesPastACompletedPlanMilestone(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Approved plan ready").EnqueueText("Plan step completed and verified").EnqueueText("The remaining global condition is now demonstrated")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"not_met","reason":"The plan is one milestone; verify the remaining global condition."}`).EnqueueText(`{"verdict":"met","reason":"Both the completed plan and remaining condition are demonstrated."}`)
	a := newGoalTestApp(t, worker, evaluator)
	configureGoalPlan(t, a, worker)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal finish the plan and the remaining condition", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 10})
	if err != nil || result.Goal.Status != goals.Achieved || result.Goal.Turns != 2 || len(worker.Calls()) != 3 || len(evaluator.Calls()) != 2 || !strings.Contains(worker.Calls()[2].Message, "remaining global condition") {
		t.Fatalf("plan completion replaced the whole goal: %+v %v", result, err)
	}
}

func TestApprovedGoalPlanRefreshesReadOnlySchemaButPreservesInvocationCeiling(t *testing.T) {
	for _, restricted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrestricted", true: "restricted"}[restricted], func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Approved plan ready").EnqueueToolCall("write", map[string]any{"file_path": "approved.txt", "content": "verified"}).EnqueueText("The approved change is verified")
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The approved change and native verification demonstrate the condition."}`)
			a := newGoalTestApp(t, worker, evaluator)
			configureGoalPlan(t, a, worker)
			a.config.Plan.Enabled = true
			a.planningModeEnabled = true
			a.planManager.SetApprovalCommitHandler(a.disablePlanModeAfterApproval)
			mutation := &directPlanMutationTool{}
			a.registry.MustRegister(mutation)
			if restricted {
				if err := a.ConfigureToolCapability([]string{"read", "write", "bash"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			approve := worker.OnSend
			worker.OnSend = func(ctx context.Context) {
				if len(worker.Calls()) == 1 {
					schema, ok := tools.ToolSchemaCeilingFromContext(ctx, a.executor)
					if !ok || strings.Contains(" "+strings.Join(schema, " ")+" ", " write ") {
						t.Error("planning request exposed a mutation", schema)
					}
				} else {
					schema, ok := tools.ToolSchemaCeilingFromContext(ctx, a.executor)
					if !ok || !strings.Contains(" "+strings.Join(schema, " ")+" ", " write ") {
						t.Error("approved execution retained the read-only schema", schema)
					}
					if restricted {
						ceiling, ok := tools.ToolCapabilityCeilingFromContext(ctx)
						if !ok || strings.Join(ceiling, ",") != "bash,read,write" || strings.Join(schema, ",") != "bash,read,write" {
							t.Error("approval widened the invocation ceiling", ceiling, schema)
						}
					}
				}
				approve(ctx)
			}
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal apply and verify the approved change", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 8})
			if err != nil || result.Goal.Status != goals.Achieved || mutation.runs != 1 {
				t.Fatalf("approved mutation did not run exactly once: result=%+v err=%v mutations=%d", result, err, mutation.runs)
			}
		})
	}
}

func TestNativePlanVerificationHonorsAnExcludedShell(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	p, step := configureGoalPlan(t, a, testkit.NewMockClient())
	shell := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("must not execute")}}
	a.registry.Unregister("bash")
	a.registry.MustRegister(shell)
	ctx := tools.ContextWithToolCapabilityCeiling(t.Context(), []string{"read", "write"})
	_, _, passed, reason := a.runStepVerificationCommands(ctx, p, step)
	if passed || !strings.Contains(reason, "capability ceiling") || shell.calls != 0 {
		t.Fatalf("native verification escaped the caller's tools: passed=%t reason=%s calls=%d", passed, reason, shell.calls)
	}
}

func TestGoalPlanSummaryIsFormattedBeforeTheVerdict(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Approved plan ready").EnqueueText("Plan step completed and verified").EnqueueText(`{"answer":"plan verified"}`)
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The verified plan and formatted terminal result demonstrate the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	configureGoalPlan(t, a, worker)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal verify the whole plan and return JSON", goalStructuredOptions(t))
	if err != nil || result.Goal.Status != goals.Achieved || result.StructuredOutput == nil || result.Result != `{"answer":"plan verified"}` || result.Goal.InputTokens != 40 || len(evaluator.Calls()) != 1 || worker.Calls()[2].ToolsCount != 0 {
		t.Fatalf("plan summary was evaluated before formatting: %+v %v", result, err)
	}
}

func TestGoalPlanFailureDoesNotScheduleAnotherWorkCycle(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Approved plan ready").EnqueueStartupError(&client.TerminalProviderError{Code: "1000", Status: 401, Message: "authentication failed"})
	evaluator := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	p, _ := configureGoalPlan(t, a, worker)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal finish the verified plan", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 8})
	if err == nil || result.Goal.Status != goals.Cleared || len(evaluator.Calls()) != 0 || p.GetStatus() == plan.StatusCompleted {
		t.Fatalf("failed plan achieved a goal or retained fatal credentials: %+v %v", result, err)
	}
	a.goalMu.Lock()
	scheduled := a.goalTimer != nil
	a.goalMu.Unlock()
	if scheduled || a.pendingCount() != 0 {
		t.Fatal("failed plan created another automatic work cycle")
	}
}

func TestGoalPlanRespectsTheSharedInvocationTurnCap(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Approved plan ready").EnqueueText("must not reach the provider")
	evaluator := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	configureGoalPlan(t, a, worker)
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal complete the verified plan", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 1})
	if err == nil || result.Error == nil || result.Error.Kind != "max_turns" || len(worker.Calls()) != 1 || len(evaluator.Calls()) != 0 || result.Goal.Status != goals.Active || !result.Goal.Suspended {
		t.Fatalf("plan exceeded or hid the shared turn limit: %+v %v", result, err)
	}
}

func TestGoalPlanUsageExcludesUnrelatedBackgroundAccounting(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Approved plan ready").EnqueueText("Plan step completed and verified")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The approved step and verification demonstrate the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	configureGoalPlan(t, a, worker)
	approve := worker.OnSend
	worker.OnSend = func(ctx context.Context) {
		approve(ctx)
		if len(worker.Calls()) == 2 {
			a.accumulateScopedAgentResultUsage(&agent.AgentResult{InputTokens: 1000, OutputTokens: 500})
		}
	}
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal finish the verified plan", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 8})
	if err != nil || result.Goal.InputTokens != 30 || result.Usage.InputTokens != 30 || a.GetTokenStats().InputTokens <= 30 {
		t.Fatalf("goal spend was derived from unrelated session totals: %+v %v", result, err)
	}
}

func TestChangedGoalCannotBeAdoptedByAnOlderPlan(t *testing.T) {
	for _, mode := range []string{"clear", "replace", "replace-with-schema"} {
		t.Run(mode, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueText("Approved original plan").EnqueueText("Old approved step completed")
			evaluator := testkit.NewMockClient()
			a := newGoalTestApp(t, worker, evaluator)
			configureGoalPlan(t, a, worker)
			approve := worker.OnSend
			worker.OnSend = func(ctx context.Context) {
				approve(ctx)
				if len(worker.Calls()) == 2 {
					if mode == "clear" {
						_, _ = a.ClearGoal(t.Context())
					} else if err := a.SetGoal(t.Context(), "replacement condition"); err != nil {
						t.Error(err)
					}
				}
			}
			opts := HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 8}
			if mode == "replace-with-schema" {
				opts = goalStructuredOptions(t)
			}
			result, _ := a.RunHeadlessWithOptions(t.Context(), "/goal complete the original plan", opts)
			if len(worker.Calls()) != 2 || len(evaluator.Calls()) != 0 || result.Goal.Status == goals.Achieved || a.session.GoalState().LastAchieved != nil {
				t.Fatalf("old plan evaluated or formatted a changed goal: %+v", result)
			}
			if mode != "clear" && (result.Goal.Condition != "replacement condition" || result.Goal.Suspended) {
				t.Fatal("old plan paused or replaced the new condition")
			}
		})
	}
}

func TestDelegatedGoalPlanUsesTheInvocationModelTurnLimit(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Approved plan ready")
	child := testkit.NewMockClient().EnqueueText("must not be requested")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	configureGoalPlan(t, a, worker)
	configureGoalPlanRunner(t, a, child)
	a.config.Plan.DelegateSteps = true
	result, err := a.RunHeadlessWithOptions(t.Context(), "/goal complete the verified plan", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 1})
	if err == nil || result.Error == nil || result.Error.Kind != "max_turns" || len(child.Calls()) != 0 || result.Goal.Status != goals.Active || !result.Goal.Suspended {
		t.Fatalf("delegated plan bypassed or hid the model-turn limit: %+v %v", result, err)
	}
}
