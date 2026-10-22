package app

import (
	"context"
	"io"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/testkit"
	"gokin/internal/ui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGoalNotificationsReleaseSessionLeaseBeforeSending(t *testing.T) {
	for _, notification := range []string{"pause", "usage-save-error"} {
		t.Run(notification, func(t *testing.T) {
			a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
			if err := a.SetGoal(t.Context(), "test condition"); err != nil {
				t.Fatal(err)
			}
			ctx, _ := a.observeGoalTurn(t.Context())
			if notification == "usage-save-error" {
				client.RecordContextTokenUsage(ctx, 10, 4)
				if err := a.sessionLease.Release(); err != nil {
					t.Fatal(err)
				}
			}
			// An undrained program holds Send open, just as an Update callback
			// waiting for the session lease would. No UI goroutine races this test.
			program := tea.NewProgram(ui.NewModel(), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithInput(nil))
			a.program = program
			done := make(chan struct{})
			go func() {
				defer close(done)
				if notification == "pause" {
					a.suspendGoal(ctx, a.session.GoalState().Current.ID, "policy changed")
				} else {
					client.CheckpointContextTokenUsage(ctx)
				}
			}()
			t.Cleanup(func() {
				program.Kill()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("goal notification did not stop")
				}
			})
			deadline := time.NewTimer(2 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				a.programSendOrderMu.Lock()
				sending := a.programSendTail != nil
				a.programSendOrderMu.Unlock()
				if sending {
					break
				}
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatal("goal notification did not reach UI delivery")
				}
			}
			if !a.sessionLeaseMu.TryLock() {
				t.Fatal("UI delivery holds the lease needed by submit: deadlock")
			}
			a.sessionLeaseMu.Unlock()
		})
	}
}

func TestGoalVerdictLeavesUIReadyForInput(t *testing.T) {
	for _, verdict := range []string{"met", "impossible", "not_met"} {
		t.Run(verdict, func(t *testing.T) {
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"` + verdict + `","reason":"Acceptance evidence checked."}`)
			a := newGoalTestApp(t, testkit.NewMockClient(), evaluator)
			if err := a.SetGoal(t.Context(), "test condition"); err != nil {
				t.Fatal(err)
			}
			if verdict == "not_met" {
				state := a.session.GoalState()
				state.Current.NoProgressTurns = 2
				a.sessionLeaseMu.Lock()
				err := a.saveGoalState(state)
				a.sessionLeaseMu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			program := tea.NewProgram(ui.NewModel(), tea.WithContext(ctx), tea.WithoutRenderer(), tea.WithoutSignalHandler(), tea.WithInput(nil), tea.WithOutput(io.Discard))
			a.program = program
			runDone := make(chan struct{})
			go func() { defer close(runDone); _, _ = program.Run() }()
			t.Cleanup(func() { program.Kill(); <-runDone })
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				a.safeSendToProgram(ui.ResponseDoneMsg{})
				a.finishGoalTurn(ctx, "Work inspected", nil)
			}()
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("goal finalization blocked")
			}
			if a.pendingCount() != 0 {
				t.Fatal("terminal or suspended verdict queued more work")
			}
			state := make(chan ui.UIDebugState, 1)
			a.safeSendToProgram(ui.DebugStateRequestMsg{Resp: state})
			select {
			case got := <-state:
				if got.State != "input" {
					t.Fatalf("finished goal left UI in %s", got.State)
				}
			case <-ctx.Done():
				t.Fatal("UI did not report its state")
			}
		})
	}
}
