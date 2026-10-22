package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/commands"
	"gokin/internal/goals"
	"gokin/internal/hooks"
	"gokin/internal/tasks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func goalTestTimer(t *testing.T, a *App) *goalScheduledAction {
	t.Helper()
	a.goalMu.Lock()
	defer a.goalMu.Unlock()
	if a.goalTimer == nil {
		t.Fatal("expected a scheduled goal action")
	}
	return a.goalTimer
}

// Drive a timer event directly without sleeping through the production retry
// delay. State and callback still go through the real save/admission boundary.
func fireGoalTestTimer(t *testing.T, a *App) {
	t.Helper()
	action := goalTestTimer(t, a)
	a.sessionLeaseMu.Lock()
	a.goalMu.Lock()
	action.timer.Stop()
	action.due = time.Now().Add(-time.Second)
	a.goalMu.Unlock()
	state := a.session.GoalState()
	if action.kind == "retry" {
		state.Current.RetryAt = action.due
	} else {
		state.Current.NextCheckIn = action.due
	}
	if err := a.saveGoalState(state); err != nil {
		a.sessionLeaseMu.Unlock()
		t.Fatal(err)
	}
	a.sessionLeaseMu.Unlock()
	a.fireGoalAction(action)
	if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
		t.Fatal("scheduled foreground did not settle")
	}
}

func failGoalFixture(t *testing.T, a *App, err error) *goalTurnObservation {
	t.Helper()
	ctx, observation := a.observeGoalTurn(t.Context())
	if observation == nil {
		t.Fatal("goal observation missing")
	}
	observation.message = "Continue the original goal directive"
	a.failGoalTurn(ctx, &observation.source, err)
	return observation
}

func TestGoalEvaluatorRetriesThreeTimesWithoutRepeatingWorker(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	worker := testkit.NewMockClient().EnqueueText("The worker has completed the requested operation")
	evaluator := testkit.NewMockClient()
	for i := 0; i < 4; i++ {
		evaluator.EnqueueStartupError(&client.HTTPError{StatusCode: 503, Message: "service unavailable"})
	}
	a := newGoalTestApp(t, worker, evaluator)
	a.commandHandler = commands.NewHandler()
	if err := a.SetGoal(t.Context(), "verify the complete operation"); err != nil {
		t.Fatal(err)
	}
	a.processMessageWithContext(t.Context(), "Perform and verify the operation")
	for i := 1; i <= 3; i++ {
		goal := a.session.GoalState().Current
		if goal.RetryAttempts != i || goal.RetryAt.IsZero() || !goal.Suspended || !goalTestTimer(t, a).source.evaluateOnly {
			t.Fatalf("retry %d was not an evaluator-only continuation: %+v", i, goal)
		}
		fireGoalTestTimer(t, a)
	}
	goal := a.session.GoalState().Current
	a.goalMu.Lock()
	scheduled := a.goalTimer != nil
	a.goalMu.Unlock()
	if scheduled || goal.RetryAttempts != 3 || !goal.RetryAt.IsZero() || goal.Status != goals.Active || !goal.Suspended || len(worker.Calls()) != 1 || len(evaluator.Calls()) != 4 {
		t.Fatalf("retry cap or worker isolation failed: %+v, scheduled=%v worker=%d evaluator=%d", goal, scheduled, len(worker.Calls()), len(evaluator.Calls()))
	}
}

func TestGoalWorkerRetryRestoresCompletedMutationCheckpoint(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	worker := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": "result.txt", "content": "once"}).EnqueueText("The original write completed")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The original write and verification demonstrate the entire condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	a.commandHandler = commands.NewHandler()
	mutation := &appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("written exactly once")}}
	if err := a.registry.Register(mutation); err != nil {
		t.Fatal(err)
	}
	if err := a.SetGoal(t.Context(), "write the result exactly once"); err != nil {
		t.Fatal(err)
	}
	history, _, err := a.executor.Execute(t.Context(), nil, "write result")
	if err != nil {
		t.Fatal(err)
	}
	a.session.SetHistory(history)
	if mutation.CallCount() != 1 {
		t.Fatal("initial mutation did not execute")
	}
	worker.EnqueueToolCall("write", map[string]any{"file_path": "result.txt", "content": "once"}).EnqueueText("Verified the same retained write")
	failGoalFixture(t, a, io.ErrUnexpectedEOF)
	if len(goalTestTimer(t, a).source.checkpoints) != 1 {
		t.Fatal("retry lost its exact completed mutation generation")
	}
	fireGoalTestTimer(t, a)
	if mutation.CallCount() != 1 || a.session.GoalState().Current.Status != goals.Achieved {
		t.Fatal("automatic retry repeated a mutation or failed to finish")
	}
	historyManager, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := historyManager.LoadFull(a.session.GetID())
	if err != nil || saved.Goal.Current.Status != goals.Achieved {
		t.Fatal("retry completion was not durable", err)
	}
}

func TestGoalRetryTimersCannotCrossUserOrConversationBoundaries(t *testing.T) {
	for _, boundary := range []string{"clear", "replace", "cancel", "user", "epoch", "shutdown", "lease", "policy", "disabled", "headless"} {
		t.Run(boundary, func(t *testing.T) {
			t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
			worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
			a := newGoalTestApp(t, worker, evaluator)
			if err := a.SetGoal(t.Context(), "complete condition"); err != nil {
				t.Fatal(err)
			}
			failGoalFixture(t, a, io.EOF)
			action := goalTestTimer(t, a)
			switch boundary {
			case "clear":
				_, _ = a.ClearGoal(t.Context())
			case "replace":
				if err := a.SetGoal(t.Context(), "replacement"); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				a.CancelProcessing()
			case "user":
				a.sessionLeaseMu.Lock()
				a.invalidateGoalUserInputLocked("new direction")
				a.sessionLeaseMu.Unlock()
			case "epoch":
				a.recoveryEpoch.Add(1)
			case "shutdown":
				a.beginShutdown()
			case "lease":
				_ = a.ReleaseSessionWriterLease()
			case "policy":
				a.hooksManager = hooks.NewManager(false, a.workDir)
			case "disabled":
				t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "0")
			case "headless":
				a.headlessRunActive = true
			}
			a.fireGoalAction(action)
			if len(worker.Calls()) != 0 || len(evaluator.Calls()) != 0 || a.pendingCount() != 0 {
				t.Fatal("old timer dispatched work across a boundary")
			}
		})
	}
}

func TestGoalZeroDisablesRetriesAndPermanentFailuresStayPaused(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		zero bool
	}{
		{"zero", io.EOF, true},
		{"rate limit", &client.HTTPError{StatusCode: 429, Message: "rate limit"}, false},
		{"usage window", &client.TerminalProviderError{Code: "1310", Status: 429, Message: "weekly usage limit"}, false},
		{"unknown", errors.New("operation requires human review"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.zero {
				t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "0")
			}
			a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
			if err := a.SetGoal(t.Context(), "entire condition"); err != nil {
				t.Fatal(err)
			}
			failGoalFixture(t, a, tc.err)
			a.goalMu.Lock()
			scheduled := a.goalTimer != nil
			a.goalMu.Unlock()
			goal := a.session.GoalState().Current
			if scheduled || !goal.Suspended || goal.RetryAttempts != 0 || goal.Status != goals.Active {
				t.Fatalf("nonretryable goal was cleared or scheduled: %+v", goal)
			}
		})
	}
}

func TestGoalIdleCheckInsBackOffAndPauseAfterThree(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires a POSIX shell")
	}
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	a := newGoalTestApp(t, worker, evaluator)
	a.commandHandler = commands.NewHandler()
	a.taskManager = tasks.NewManager(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		a.taskManager.CancelAll()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = a.taskManager.WaitAll(wait)
		if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
			t.Error("check-in worker did not stop")
		}
	})
	if _, err := a.taskManager.StartWithArgs(ctx, sh, []string{"-c", "sleep 30"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetGoal(t.Context(), "inspect all remaining background work"); err != nil {
		t.Fatal(err)
	}
	a.waitForGoalBackground(t.Context(), a.session.GoalState().Current, a.captureConversationLineage(), a.runningGoalTasks())
	for i := 1; i <= 3; i++ {
		worker.EnqueueText("The retained output shows the task is still making progress")
		fireGoalTestTimer(t, a)
		if len(worker.Calls()) != i || !strings.Contains(worker.Calls()[i-1].Message, "check-in") {
			t.Fatal("idle check-in did not start a normal worker turn")
		}
		state := a.session.GoalState()
		if state.Current.IdleCheckIns != i || state.Current.Status != goals.Active || !state.Current.Suspended || state.Current.Turns != 0 || len(evaluator.Calls()) != 0 {
			t.Fatal("idle check-in count was lost")
		}
		if i == 3 && !strings.Contains(worker.Calls()[i-1].Message, "third idle check-in") {
			t.Fatal("third check-in did not announce its cap")
		}
	}
	a.goalMu.Lock()
	scheduled := a.goalTimer != nil
	a.goalMu.Unlock()
	if scheduled {
		t.Fatal("a fourth idle check-in was scheduled")
	}
	worker.EnqueueText("The user asked for a new progress check")
	a.processMessageWithContext(t.Context(), "Check the progress now")
	if a.session.GoalState().Current.IdleCheckIns != 0 {
		t.Fatal("new user input did not reset the idle cap")
	}
	if goalTestTimer(t, a).kind != "checkin" {
		t.Fatal("new user input did not re-enable idle checks")
	}
}

func TestGoalCheckInWithMissingCompletionContinuesAndEvaluates(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	worker := testkit.NewMockClient().EnqueueText("The retained task result proves the condition")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"Observed evidence demonstrates the complete condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	if err := a.SetGoal(t.Context(), "inspect the missing task result"); err != nil {
		t.Fatal(err)
	}
	a.sessionLeaseMu.Lock()
	state := a.session.GoalState()
	goals.DeferBackground(state.Current, time.Now(), time.Minute)
	state.Current.Suspended = true
	err := a.saveGoalState(state)
	if err == nil {
		a.armGoalTimerLocked(&goalScheduledAction{source: goalContinuation{sessionID: a.session.GetID(), goalID: state.Current.ID, epoch: a.recoveryEpoch.Load(), turns: state.Current.Turns, scheduled: true, intent: a.goalIntent.Load()}, kind: "checkin", due: state.Current.NextCheckIn})
	}
	a.sessionLeaseMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	fireGoalTestTimer(t, a)
	if a.session.GoalState().Current.Status != goals.Achieved || len(worker.Calls()) != 1 || !strings.Contains(worker.Calls()[0].Message, "stopped without reporting") {
		t.Fatal("a missing completion callback left the goal stalled")
	}
}

func TestGoalDueCheckInDeliveredAtTurnEndInBothModes(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	for _, headless := range []bool{false, true} {
		t.Run(map[bool]string{false: "interactive", true: "headless"}[headless], func(t *testing.T) {
			a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
			if err := a.SetGoal(t.Context(), "inspect pending work"); err != nil {
				t.Fatal(err)
			}
			state := a.session.GoalState()
			state.Current.WaitingSince = time.Now().Add(-time.Hour)
			state.Current.NextCheckIn = time.Now().Add(-time.Second)
			if err := a.session.SetGoalState(state); err != nil {
				t.Fatal(err)
			}
			a.processing = true
			a.headlessDirect = headless
			a.headlessRunActive = headless
			a.waitForGoalBackground(t.Context(), state.Current, a.captureConversationLineage(), map[string]bool{"shell:pending": true})
			var next *goalContinuation
			if headless {
				next = a.takeGoalContinuation()
			} else {
				request, _, _ := a.dequeuePendingRequest()
				next = request.goal
			}
			if next == nil || !next.checkIn || !strings.Contains(next.prompt, "check-in") || a.session.GoalState().Current.IdleCheckIns != 0 {
				t.Fatal("due turn-end check-in was dropped or counted as idle")
			}
			a.goalMu.Lock()
			scheduled := a.goalTimer != nil
			a.goalMu.Unlock()
			if scheduled {
				t.Fatal("turn-end delivery also installed an idle timer")
			}
		})
	}
}

func TestGoalIdleTimerFiresThroughRealForegroundAdmission(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	worker := testkit.NewMockClient().EnqueueText("Verified the retained result")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The retained result demonstrates the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	if err := a.SetGoal(t.Context(), "inspect a lost completion"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	worker.OnSend = func(context.Context) { close(started) }
	a.sessionLeaseMu.Lock()
	state := a.session.GoalState()
	goals.DeferBackground(state.Current, time.Now(), time.Minute)
	state.Current.NextCheckIn = time.Now().Add(30 * time.Millisecond)
	state.Current.Suspended = true
	err := a.saveGoalState(state)
	if err == nil {
		a.armGoalTimerLocked(&goalScheduledAction{source: goalContinuation{sessionID: a.session.GetID(), goalID: state.Current.ID, epoch: a.recoveryEpoch.Load(), turns: state.Current.Turns, scheduled: true, intent: a.goalIntent.Load()}, kind: "checkin", due: state.Current.NextCheckIn})
	}
	a.sessionLeaseMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("idle timer did not launch")
	}
	if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
		t.Fatal("idle timer worker did not finish")
	}
	goal := a.session.GoalState().Current
	if goal.Status != goals.Achieved || goal.IdleCheckIns != 1 {
		t.Fatalf("timer did not deliver and independently evaluate: %+v", goal)
	}
}

func TestGoalRetryRejectsUnserializableCheckpoint(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	if err := a.SetGoal(t.Context(), "verify the operation"); err != nil {
		t.Fatal(err)
	}
	a.executor.GetCheckpointJournal().Record(&genai.FunctionCall{ID: "mutation", Name: "write", Args: map[string]any{"file_path": "result"}}, tools.ToolResult{Success: true, Content: "completed", Data: func() {}})
	failGoalFixture(t, a, io.EOF)
	a.goalMu.Lock()
	scheduled := a.goalTimer != nil
	a.goalMu.Unlock()
	goal := a.session.GoalState().Current
	if scheduled || goal.RetryAttempts != 0 || !goal.Suspended || !strings.Contains(goal.Reason, "automatic retry blocked") {
		t.Fatalf("unsafe replay was scheduled: %+v", goal)
	}
}

func TestGoalCancellationDuringWorkerPausesWithoutARegisteredTimer(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	if err := a.SetGoal(t.Context(), "complete the operation"); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.processing = true
	ctx := a.claimForegroundContextLocked()
	a.mu.Unlock()
	a.CancelProcessing()
	goal := a.session.GoalState().Current
	if ctx.Err() == nil || goal.Status != goals.Active || !goal.Suspended || !strings.Contains(goal.Reason, "Paused by the user") {
		t.Fatalf("foreground cancellation left the goal appearing active: %+v", goal)
	}
}

func TestGoalRetrySnapshotOwnsNestedArgumentsAndExactIntegers(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	if err := a.SetGoal(t.Context(), "verify the mutation"); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"file_path": "result", "options": map[string]any{"id": json.Number("9007199254740993"), "mode": "original"}}
	a.executor.GetCheckpointJournal().Record(&genai.FunctionCall{ID: "mutation", Name: "write", Args: args}, tools.NewSuccessResult("completed once"))
	failGoalFixture(t, a, io.EOF)
	action := goalTestTimer(t, a)
	args["options"].(map[string]any)["mode"] = "mutated later"
	options := action.source.checkpoints[0].Args["options"].(map[string]any)
	if options["mode"] != "original" || options["id"] != json.Number("9007199254740993") {
		t.Fatal("delayed retry shares mutable data or lost integer precision", options)
	}
}

func TestGoalRetryDoesNotExpandReferencesInAnAlreadyPreparedPrompt(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	worker := testkit.NewMockClient().EnqueueText("The retained operation is verified")
	evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The observed result demonstrates the condition."}`)
	a := newGoalTestApp(t, worker, evaluator)
	if err := os.WriteFile(filepath.Join(a.workDir, "literal.txt"), []byte("UNREQUESTED_REFERENCE_CONTENT"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.SetGoal(t.Context(), "verify the complete operation"); err != nil {
		t.Fatal(err)
	}
	ctx, observation := a.observeGoalTurn(t.Context())
	observation.message = "The already-expanded source contains a literal @literal.txt token"
	a.failGoalTurn(ctx, &observation.source, io.EOF)
	fireGoalTestTimer(t, a)
	if len(worker.Calls()) != 1 || worker.Calls()[0].Message != observation.message {
		t.Fatal("automatic retry re-read or changed the already-prepared prompt")
	}
}

func TestSlowGoalCheckInCannotBypassIdleCapAtTurnEnd(t *testing.T) {
	t.Setenv("GOKIN_GOAL_CHECKIN_MINUTES", "30")
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires a POSIX shell")
	}
	worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
	for i := 0; i < 4; i++ {
		worker.EnqueueText("The background task continues making progress")
	}
	a := newGoalTestApp(t, worker, evaluator)
	a.commandHandler = commands.NewHandler()
	a.taskManager = tasks.NewManager(t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() {
		cancel()
		a.taskManager.CancelAll()
		wait, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = a.taskManager.WaitAll(wait)
		if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
			t.Error("check-in did not stop")
		}
	})
	if _, err := a.taskManager.StartWithArgs(ctx, sh, []string{"-c", "sleep 30"}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetGoal(t.Context(), "inspect progress without an endless check-in chain"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	third := make(chan struct{})
	worker.OnSend = func(context.Context) {
		// Simulate a model response taking longer than the next interval. Each
		// turn ends overdue, so a mistaken turn-end continuation would evade
		// the idle counter and run until the mock queue is exhausted.
		a.sessionLeaseMu.Lock()
		state := a.session.GoalState()
		state.Current.NextCheckIn = time.Now().Add(-time.Second)
		if err := a.saveGoalState(state); err != nil {
			t.Error(err)
		}
		a.sessionLeaseMu.Unlock()
		if calls.Add(1) == 3 {
			close(third)
		}
	}
	a.waitForGoalBackground(t.Context(), a.session.GoalState().Current, a.captureConversationLineage(), a.runningGoalTasks())
	fireGoalTestTimer(t, a)
	select {
	case <-third:
	case <-time.After(5 * time.Second):
		t.Fatal("three bounded idle check-ins did not arrive")
	}
	if !a.foregroundWorkers.WaitWithTimeout(5 * time.Second) {
		t.Fatal("slow check-in did not settle")
	}
	a.goalMu.Lock()
	scheduled := a.goalTimer != nil
	a.goalMu.Unlock()
	goal := a.session.GoalState().Current
	if scheduled || calls.Load() != 3 || goal.IdleCheckIns != 3 || goal.Turns != 0 || a.pendingCount() != 0 || len(evaluator.Calls()) != 0 {
		t.Fatalf("slow response bypassed the idle cap: %+v, calls=%d", goal, calls.Load())
	}
}
