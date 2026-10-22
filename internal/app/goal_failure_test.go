package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/goals"
	"gokin/internal/testkit"
)

func TestUnrecoverableGoalErrorDistinguishesUsageWindowsAndCredits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		clear bool
	}{
		{"auth", &client.HTTPError{StatusCode: 401, Message: "invalid key"}, true},
		{"glm balance", &client.TerminalProviderError{Code: "1113", Status: 400, Message: "insufficient balance"}, true},
		{"glm model", &client.TerminalProviderError{Code: "1211", Status: 400}, true},
		{"glm context", &client.TerminalProviderError{Code: "1261", Status: 400}, true},
		{"deepseek auth", &client.TerminalProviderError{Code: "deepseek_authentication_failed", Status: 401}, true},
		{"model", &client.APIError{StatusCode: 404, Message: "model_not_found"}, true},
		{"credits", &client.HTTPError{StatusCode: 400, Message: "Your credit balance is too low"}, true},
		{"context", &client.HTTPError{StatusCode: 400, Message: "prompt is too long"}, true},
		{"usage window", &client.TerminalProviderError{Code: "1310", Status: 429, Message: "weekly limit"}, false},
		{"ambiguous quota", &client.TerminalProviderError{Code: "1308", Status: 429, Message: "usage limit"}, false},
		{"deepseek balance", &client.TerminalProviderError{Code: "deepseek_insufficient_balance", Status: 402}, true},

		{"content", &client.TerminalProviderError{Code: "1301", Status: 400}, false},
		{"rate", &client.HTTPError{StatusCode: 429, Message: "rate limit"}, false},
		{"overload", &client.HTTPError{StatusCode: 529, Message: "overloaded"}, false},
		{"unrelated 404", &client.HTTPError{StatusCode: 404, Message: "proxy route not found"}, false},
		{"local error text", errors.New("hook says insufficient balance and model_not_found"), false},
		{"cancel", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unrecoverableGoalError(fmt.Errorf("wrapped request: %w", tc.err)); got != tc.clear {
				t.Fatalf("clear = %v, want %v", got, tc.clear)
			}
		})
	}
}

func TestHeadlessGoalFailureClearsOnlyUnrecoverableWorkerOrEvaluatorError(t *testing.T) {
	for _, stage := range []string{"worker", "evaluator"} {
		for _, fatal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fatal=%v", stage, fatal), func(t *testing.T) {
				worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
				err := error(&client.TerminalProviderError{Code: "1310", Status: 429, Message: "weekly usage limit"})
				if fatal {
					err = &client.TerminalProviderError{Code: "1113", Status: 400, Message: "insufficient balance"}
				}
				if stage == "worker" {
					worker.EnqueueStartupError(err)
				} else {
					worker.EnqueueText("Worker finished; independently verify the goal.")
					evaluator.EnqueueStartupError(err)
				}
				a := newGoalTestApp(t, worker, evaluator)
				result, runErr := a.RunHeadlessWithOptions(t.Context(), "/goal verify the entire condition", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 5})
				if runErr == nil || result.Goal == nil {
					t.Fatalf("failed model turn reported success: %+v, %v", result, runErr)
				}
				goal := result.Goal
				if fatal && goal.Status != goals.Cleared || !fatal && (goal.Status != goals.Active || !goal.Suspended) {
					t.Fatalf("wrong goal failure transition: %+v", goal)
				}
				if len(worker.Calls()) != 1 || len(evaluator.Calls()) != map[string]int{"worker": 0, "evaluator": 1}[stage] || len(a.session.GetPendingRecoveries()) != 0 {
					t.Fatal("goal failure spawned another request or an unowned recovery")
				}
				history, err := chat.NewHistoryManager()
				if err != nil {
					t.Fatal(err)
				}
				saved, err := history.LoadFull(a.session.GetID())
				if err != nil || saved.Goal.Current.Status != goal.Status || saved.Goal.Current.Suspended != goal.Suspended {
					t.Fatal("goal failure state was not durable", err)
				}
			})
		}
	}
}

func TestOldGoalTurnCannotFailOrEvaluateReplacement(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			worker, evaluator := testkit.NewMockClient(), testkit.NewMockClient()
			if failed {
				worker.EnqueueStartupError(&client.TerminalProviderError{Code: "1113", Message: "old balance error"})
			} else {
				worker.EnqueueText("Old task finished")
			}
			a := newGoalTestApp(t, worker, evaluator)
			worker.OnSend = func(context.Context) {
				if err := a.SetGoal(t.Context(), "replacement condition"); err != nil {
					t.Error(err)
				}
			}
			_, _ = a.RunHeadlessWithOptions(t.Context(), "/goal original condition", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
			goal := a.session.GoalState().Current
			if goal.Condition != "replacement condition" || goal.Status != goals.Active || goal.Suspended || len(evaluator.Calls()) != 0 {
				t.Fatalf("old worker modified or evaluated a replacement: %+v", goal)
			}
		})
	}
}

func TestGoalOnlySessionIsDurableWork(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	if err := a.SetGoal(t.Context(), "saved condition before the first turn"); err != nil {
		t.Fatal(err)
	}
	if !sessionStateHasDurableWork(a.session.GetState()) {
		t.Fatal("fresh goal was treated as an empty disposable session")
	}
}
