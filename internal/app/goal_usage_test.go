package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/goals"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestDetachedGoalAgentUsageIsDurableBeforeAndAfterCompletion(t *testing.T) {
	worker := testkit.NewMockClient().
		EnqueueToolCall("task", map[string]any{"subagent_type": "general", "prompt": "inspect the acceptance proof", "run_in_background": true, "max_turns": 3}).
		EnqueueText("Waiting for the acceptance proof").EnqueueText("The completed background proof demonstrates the condition")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The completed background proof demonstrates the full condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	child := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "notes"}).EnqueueText("BACKGROUND_ACCEPTANCE_PROOF")
	ready, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var childCtx context.Context
	child.OnSend = func(ctx context.Context) {
		if len(child.Calls()) == 2 {
			childCtx = ctx
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}
	worker.OnSend = func(ctx context.Context) {
		if len(worker.Calls()) == 2 {
			select {
			case <-ready:
			case <-ctx.Done():
			}
		}
	}
	runner := configureGoalPlanRunner(t, a, child)
	t.Cleanup(func() { runner.CancelAll(); a.foregroundWorkers.WaitWithTimeout(5 * time.Second) })
	runner.SetOnAgentComplete(func(id string, result *agent.AgentResult) {
		a.executor.AddPendingNotification(result.Output)
		a.goalBackgroundCompleted("agent:"+id, result.Output)
		close(completed)
	})
	task := tools.NewTaskTool()
	task.SetRunner(&agentRunnerAdapter{runner: runner})
	task.SetAgentTypeProvider(agent.NewAgentTypeRegistry())
	a.registry.MustRegister(task)
	if err := a.SetGoal(ctx, "verify the completed background proof"); err != nil {
		t.Fatal(err)
	}
	a.processMessageWithContext(ctx, "Start the background inspection")
	goal := a.session.GoalState().Current
	if goal.InputTokens != 30 || !goal.Suspended || len(evaluator.Calls()) != 0 {
		t.Fatalf("in-flight agent usage was lost or evaluated early: %+v", goal)
	}
	a.mu.Lock()
	a.processing = true // Keep the completion actor queued for deterministic inspection.
	a.mu.Unlock()
	close(release)
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("background agent did not finish")
	}
	goal = a.session.GoalState().Current
	if goal.InputTokens != 40 || goal.Turns != 0 {
		t.Fatalf("late model usage was lost or counted twice: %+v", goal)
	}
	client.CheckpointContextTokenUsage(childCtx)
	client.CheckpointContextTokenUsage(childCtx)
	history, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := history.LoadFull(a.session.GetID())
	if err != nil || saved.Goal.Current.InputTokens != 40 {
		t.Fatalf("background usage was not saved exactly once: %+v %v", saved, err)
	}
	request, _, ok := a.dequeuePendingRequest()
	if !ok || request.goal == nil {
		t.Fatal("agent completion did not queue the independent goal turn")
	}
	a.processMessageWithContext(context.WithValue(ctx, goalContinuationKey{}, request.goal), request.message)
	goal = a.session.GoalState().Current
	if goal.Status != goals.Achieved || goal.InputTokens != 60 || a.session.GoalState().LastAchieved.InputTokens != 60 || goal.Turns != 1 {
		t.Fatalf("completed goal lost background/foreground/evaluator usage: %+v", goal)
	}
}

func TestLateGoalUsageCannotEnterAnotherMeasurementPeriod(t *testing.T) {
	for _, boundary := range []string{"replace", "resume", "different-session"} {
		t.Run(boundary, func(t *testing.T) {
			a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
			if err := a.SetGoal(t.Context(), "original condition"); err != nil {
				t.Fatal(err)
			}
			ctx, _ := a.observeGoalTurn(t.Context())
			client.RecordContextTokenUsage(ctx, 10, 4)
			client.CheckpointContextTokenUsage(ctx)
			switch boundary {
			case "replace":
				if err := a.SetGoal(t.Context(), "new condition"); err != nil {
					t.Fatal(err)
				}
			case "resume":
				a.sessionLeaseMu.Lock()
				err := a.saveGoalState(goals.Resume(a.session.GoalState(), time.Now()))
				a.sessionLeaseMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "different-session":
				a.session.SetID("different-session")
			}
			before := a.session.GoalState().Current.InputTokens
			client.RecordContextTokenUsage(ctx, 1000, 200)
			client.CheckpointContextTokenUsage(ctx)
			if got := a.session.GoalState().Current.InputTokens; got != before {
				t.Fatalf("late usage crossed %s: got=%d want=%d", boundary, got, before)
			}
		})
	}
}

func TestGoalUsageCheckpointRetainsUncommittedUsageAfterSaveFailure(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	if err := a.SetGoal(t.Context(), "durable accounting"); err != nil {
		t.Fatal(err)
	}
	ctx, _ := a.observeGoalTurn(t.Context())
	client.RecordContextTokenUsage(ctx, 10, 4)
	if err := a.sessionLease.Release(); err != nil {
		t.Fatal(err)
	}
	client.CheckpointContextTokenUsage(ctx)
	if goal := a.session.GoalState().Current; goal.InputTokens != 0 || a.goalUsage.input != 0 {
		t.Fatalf("failed save published or discarded provisional usage: %+v", goal)
	}
	a.sessionLease = nil
	client.RecordContextTokenUsage(ctx, 20, 7)
	client.CheckpointContextTokenUsage(ctx)
	client.CheckpointContextTokenUsage(ctx)
	if goal := a.session.GoalState().Current; goal.InputTokens != 30 || goal.OutputTokens != 11 {
		t.Fatalf("retry lost or duplicated pending usage: %+v", goal)
	}
}
