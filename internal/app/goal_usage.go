package app

import (
	"context"
	"time"

	"gokin/internal/client"
	"gokin/internal/goals"
	"gokin/internal/ui"
)

// One runtime account belongs to a goal's measurement period. Contexts retain
// that exact account across asynchronous work and intentional plan boundaries.
// A resumed goal starts a new measurement period, even if its ID is unchanged.
type goalUsageAccount struct {
	sessionID, goalID string
	startedAt         time.Time
	counter           client.TokenUsageCounter
	input, output     int // durable high-water marks, protected by sessionLeaseMu
}

func (account *goalUsageAccount) matches(sessionID string, goal *goals.Goal) bool {
	return account != nil && goal != nil && account.sessionID == sessionID && account.goalID == goal.ID && account.startedAt.Equal(goal.StartedAt)
}

func (a *App) withGoalUsageContext(ctx context.Context, account *goalUsageAccount) context.Context {
	ctx = client.ContextWithTokenUsageCounter(ctx, &account.counter)
	return client.ContextWithTokenUsageCheckpoint(ctx, func() {
		a.sessionLeaseMu.Lock()
		if a.session == nil || a.goalUsage != account {
			a.sessionLeaseMu.Unlock()
			return
		}
		state := a.session.GoalState()
		if !account.matches(a.session.GetID(), state.Current) {
			a.sessionLeaseMu.Unlock()
			return
		}
		input, output := account.counter.Snapshot()
		if input == account.input && output == account.output {
			a.sessionLeaseMu.Unlock()
			return
		}
		err := a.saveGoalState(state)
		a.sessionLeaseMu.Unlock()
		if err != nil {
			a.recordHeadlessTerminalOutcome("persistence_failed", err.Error())
			a.safeSendToProgram(ui.ErrorMsg(err))
		}
	})
}

// Work bound to an account is merged by saveGoalState, including late agents.
// Keep the accumulator fallback for internal callers without an observed turn.
func goalWorkerUsage(ctx context.Context, worker *turnUsageAccumulator) (int, int) {
	if observation := goalObservation(ctx); observation != nil && observation.account != nil {
		return 0, 0
	}
	if worker == nil {
		return 0, 0
	}
	return worker.input, worker.output
}
