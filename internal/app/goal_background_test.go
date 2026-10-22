package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/agent"
	"gokin/internal/commands"
	"gokin/internal/goals"
	"gokin/internal/hooks"
	"gokin/internal/router"
	"gokin/internal/tasks"
	"gokin/internal/testkit"
)

func TestGoalBackgroundShellCompletionStartsAndEvaluatesNextTurn(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires a POSIX shell")
	}
	worker := testkit.NewMockClient().EnqueueText("Waiting for the background check.").EnqueueText("The background check proves the condition.")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The completed background check demonstrates the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	a.commandHandler = commands.NewHandler()
	a.taskManager = tasks.NewManager(t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(func() {
		cancel()
		a.taskManager.CancelAll()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := a.taskManager.WaitAll(wait); err != nil {
			t.Error(err)
		}
		if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
			t.Error("background goal turn did not finish")
		}
	})
	completed := make(chan struct{})
	a.taskManager.SetCompletionHandler(func(task *tasks.Task) {
		info := task.GetInfo()
		msg := "Background result: " + info.Output
		a.executor.AddPendingNotification(msg)
		a.goalBackgroundCompleted("shell:"+info.ID, msg)
		close(completed)
	})
	barrier := filepath.Join(t.TempDir(), "finish")
	_, err = a.taskManager.StartWithArgs(ctx, sh, []string{"-c", `while [ ! -f "$1" ]; do sleep 0.01; done; printf 'BACKGROUND_PROOF_42'`, "goal-test", barrier})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetGoal(t.Context(), "verify the background check"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.processing = true
	a.mu.Unlock()
	a.processMessageWithContext(ctx, "Inspect the check")
	if goal := a.session.GoalState().Current; !goal.Suspended || goal.Turns != 0 || len(evaluator.Calls()) != 0 {
		t.Fatalf("running background work was evaluated: %+v", goal)
	}
	if err := os.WriteFile(barrier, []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-ctx.Done():
		t.Fatal("background completion callback did not arrive")
	}
	if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
		t.Fatal("automatic goal continuation did not finish")
	}
	goal := a.session.GoalState().Current
	if goal.Status != goals.Achieved || goal.Turns != 1 || len(worker.Calls()) != 2 || len(evaluator.Calls()) != 1 {
		t.Fatalf("completion did not resume and independently evaluate: %+v, worker=%d evaluator=%d", goal, len(worker.Calls()), len(evaluator.Calls()))
	}
	if goal.InputTokens != 30 || goal.OutputTokens <= 0 {
		t.Fatalf("deferred worker spend was lost: %+v", goal)
	}
	if !strings.Contains(worker.Calls()[1].Message, "BACKGROUND_PROOF_42") {
		t.Fatal("continuation did not deliver the task result before its first model request")
	}
}

type goalRoutingTrap struct{ calls int }

func (s *goalRoutingTrap) Spawn(context.Context, string, string, int, string) (string, error) {
	s.calls++
	return "unexpected", nil
}
func (s *goalRoutingTrap) SpawnAsync(context.Context, string, string, int, string) string {
	s.calls++
	return "unexpected"
}
func (*goalRoutingTrap) GetResult(string) (*agent.AgentResult, bool) { return nil, false }

func TestGoalContinuationConsumesBackgroundResultInForegroundExecutor(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Verified the completed background task result")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The observed result demonstrates the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	trap := &goalRoutingTrap{}
	a.taskRouter = router.NewRouter(nil, a.executor, trap, a.client, a.registry, false, a.workDir)
	fixtureGoalBackgroundWait(t, a)
	a.processing = true
	a.goalBackgroundCompleted("shell:original", "Background shell check finished successfully")
	request, _, ok := a.dequeuePendingRequest()
	if !ok || request.goal == nil {
		t.Fatal("completion did not create the expected goal turn")
	}
	ctx := context.WithValue(t.Context(), goalContinuationKey{}, request.goal)
	a.processMessageWithContext(ctx, request.message)
	if trap.calls != 0 || len(worker.Calls()) != 1 || len(evaluator.Calls()) != 1 || a.session.GoalState().Current.Status != goals.Achieved {
		t.Fatal("a generated background completion directive spawned another background agent")
	}
}

func fixtureGoalBackgroundWait(t *testing.T, a *App) {
	t.Helper()
	if err := a.SetGoal(t.Context(), "complete the background condition"); err != nil {
		t.Fatal(err)
	}
	state := a.session.GoalState()
	state.Current.Suspended = true
	state.Current.Reason = "Waiting for background work before evaluation"
	if err := a.session.SetGoalState(state); err != nil {
		t.Fatal(err)
	}
	a.goalWaiting = &goalBackgroundWait{source: goalContinuation{sessionID: a.session.GetID(), goalID: state.Current.ID, epoch: a.recoveryEpoch.Load(), turns: state.Current.Turns}, tasks: map[string]bool{"shell:original": true}}
}

func TestGoalBackgroundCompletionCannotCrossCancellationOrGoalBoundary(t *testing.T) {
	for _, boundary := range []string{"clear", "replace", "epoch", "cancel", "shutdown", "headless", "policy", "different-task"} {
		t.Run(boundary, func(t *testing.T) {
			worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
			a := newGoalTestApp(t, worker, evaluator)
			fixtureGoalBackgroundWait(t, a)
			key := "shell:original"
			switch boundary {
			case "clear":
				_, _ = a.ClearGoal(t.Context())
			case "replace":
				if err := a.SetGoal(t.Context(), "new condition"); err != nil {
					t.Fatal(err)
				}
			case "epoch":
				a.recoveryEpoch.Add(1)
			case "cancel":
				a.CancelProcessing()
			case "shutdown":
				a.beginShutdown()
			case "headless":
				a.headlessRunActive = true
			case "policy":
				a.hooksManager = hooks.NewManager(false, a.workDir)
			case "different-task":
				key = "shell:other"
			}
			a.goalBackgroundCompleted(key, "late result")
			if len(worker.Calls()) != 0 || len(evaluator.Calls()) != 0 || a.pendingCount() != 0 {
				t.Fatal("late background callback started work across a boundary")
			}
			if goal := a.session.GoalState().Current; goal.Status == goals.Achieved {
				t.Fatal("task completion achieved a goal without evaluation")
			}
		})
	}
}

func TestGoalBackgroundCompletionPreservesUserPriorityAndCoalescesCallbacks(t *testing.T) {
	for _, user := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "user queued"}[user], func(t *testing.T) {
			a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
			fixtureGoalBackgroundWait(t, a)
			a.processing = true
			if user {
				a.enqueuePending("my next request")
			}
			a.goalBackgroundCompleted("shell:original", "finished output")
			a.goalBackgroundCompleted("shell:original", "duplicate completion")
			request, _, ok := a.dequeuePendingRequest()
			if !ok || a.pendingCount() != 0 {
				t.Fatal("completion was lost or duplicated")
			}
			if user && (request.message != "my next request" || request.goal != nil) || !user && (request.goal == nil || !strings.Contains(request.message, "finished output")) {
				t.Fatal("background callback overtook user input or lost result data")
			}
		})
	}
}

func TestGoalBackgroundCompletionBeforeWaitPublicationIsReconciled(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	if err := a.SetGoal(t.Context(), "inspect a finished task"); err != nil {
		t.Fatal(err)
	}
	a.processing = true
	// The first snapshot saw this task, but it completed and its callback ran
	// before the waiter was published. The second snapshot must wake the goal.
	a.waitForGoalBackground(t.Context(), a.session.GoalState().Current, a.captureConversationLineage(), map[string]bool{"shell:already-finished": true})
	request, _, ok := a.dequeuePendingRequest()
	if !ok || request.goal == nil || a.session.GoalState().Current.Suspended {
		t.Fatal("completion racing wait publication left the goal paused forever")
	}
}

func TestGoalBackgroundTaskStartedDuringEvaluationDefersVerdict(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires a POSIX shell")
	}
	worker := testkit.NewMockClient().EnqueueText("Work is ready for independent evaluation")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The worker demonstrated the original condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	a.taskManager = tasks.NewManager(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		a.taskManager.CancelAll()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := a.taskManager.WaitAll(wait); err != nil {
			t.Error(err)
		}
	})
	evaluator.OnSend = func(context.Context) {
		if _, err := a.taskManager.StartWithArgs(ctx, sh, []string{"-c", "sleep 30"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SetGoal(t.Context(), "verify the whole condition"); err != nil {
		t.Fatal(err)
	}
	a.processing = true
	a.processMessageWithContext(ctx, "Complete the work")
	state := a.session.GoalState()
	if state.Current.Status != goals.Active || !state.Current.Suspended || state.Current.Turns != 0 || state.LastAchieved != nil || state.Current.InputTokens != 20 {
		t.Fatalf("a task starting during evaluation was ignored or its spend was lost: %+v", state.Current)
	}
	a.goalMu.Lock()
	waiting := a.goalWaiting != nil
	a.goalMu.Unlock()
	if !waiting {
		t.Fatal("discarding a verdict did not register a completion wakeup")
	}
}
