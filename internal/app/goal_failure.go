package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gokin/internal/client"
	"gokin/internal/goals"
	"gokin/internal/ui"
)

type goalTurnObservationKey struct{}

// Only the outcome of the whole foreground turn controls the goal. A failed
// optional review or an exchange recovered by the ordinary in-turn retries
// must not clear an otherwise successful goal.
type goalTurnObservation struct {
	source        goalContinuation
	ctx           context.Context
	err           error
	finished      bool
	worker        *turnUsageAccumulator
	input, output int
	message       string
	evaluating    bool
	account       *goalUsageAccount
}

func goalObservation(ctx context.Context) *goalTurnObservation {
	observation, _ := ctx.Value(goalTurnObservationKey{}).(*goalTurnObservation)
	return observation
}

func (a *App) observeGoalTurn(ctx context.Context) (context.Context, *goalTurnObservation) {
	if isStructuredOutputCorrection(ctx) {
		return ctx, nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if a.session == nil {
		return ctx, nil
	}
	if source, ok := ctx.Value(goalContinuationKey{}).(*goalContinuation); ok && !a.goalSourceMatchesLocked(source) {
		return ctx, nil
	}
	if _, automatic := ctx.Value(goalContinuationKey{}).(*goalContinuation); !automatic {
		a.goalIntent.Add(1)
		a.stopGoalTimer()
		a.replaceQueuedGoal(nil)
	}
	goal := a.session.GoalState().Current
	if goal == nil || goal.Status != goals.Active {
		return ctx, nil
	}
	if !a.goalUsage.matches(a.session.GetID(), goal) {
		a.goalUsage = &goalUsageAccount{sessionID: a.session.GetID(), goalID: goal.ID, startedAt: goal.StartedAt}
	}
	ctx = a.withGoalUsageContext(ctx, a.goalUsage)
	observation := &goalTurnObservation{ctx: ctx, source: goalContinuation{
		sessionID: a.session.GetID(), epoch: a.recoveryEpoch.Load(), goalID: goal.ID, turns: goal.Turns, intent: a.goalIntent.Load(),
	}, account: a.goalUsage}
	observation.ctx = context.WithValue(ctx, goalTurnObservationKey{}, observation)
	return observation.ctx, observation
}

func (a *App) finishInterruptedGoalTurn(observation *goalTurnObservation) {
	if observation == nil || observation.finished {
		return
	}
	err := observation.err
	if err == nil {
		err = observation.ctx.Err()
	}
	if err == nil {
		err = fmt.Errorf("turn stopped before goal evaluation; send a message to continue")
	}
	a.failGoalTurn(observation.ctx, &observation.source, err)
}

// This deliberately differs from IsTerminalProviderError: a content rejection,
// invalid parameter, or renewable usage-window limit pauses a goal. Only an
// authentication failure, exhausted credits, unavailable model, or unresolved
// context overflow clears it. Gokin owns its CLI credentials.
func unrecoverableGoalError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if client.IsContextTooLongError(err) {
		return true
	}
	var terminal *client.TerminalProviderError
	if errors.As(err, &terminal) {
		switch terminal.Code {
		case "1000", "1001", "1003", "1005", "1113", "1211", "1212", "1261", "deepseek_authentication_failed", "deepseek_insufficient_balance":
			return true
		}
		// Typed usage-window limits may share a billing HTTP status. They are
		// retained so the user can continue after the window resets.
		return false
	}
	status, message := 0, ""
	var httpErr *client.HTTPError
	var apiErr *client.APIError
	if errors.As(err, &httpErr) {
		status, message = httpErr.StatusCode, httpErr.Message
	} else if errors.As(err, &apiErr) {
		status, message = apiErr.StatusCode, apiErr.Message
	} else {
		return false
	}
	if status == 401 {
		return true
	}
	message = strings.ToLower(message)
	for _, marker := range []string{"insufficient balance", "insufficient credit", "credit balance", "credits exhausted", "balance exhausted", "billing_hard_limit_reached"} {
		if strings.Contains(message, marker) {
			return status >= 400 && status < 500
		}
	}
	if status == 400 || status == 403 || status == 404 {
		for _, marker := range []string{"model_not_found", "model not found", "model is unavailable", "model is not available", "model does not exist", "do not have access to the model"} {
			if strings.Contains(message, marker) {
				return true
			}
		}
	}
	return false
}

func (a *App) goalSourceMatchesLocked(source *goalContinuation) bool {
	if source == nil || a.session == nil || a.session.GetID() != source.sessionID || a.recoveryEpoch.Load() != source.epoch {
		return false
	}
	if source.scheduled && source.intent != a.goalIntent.Load() {
		return false
	}
	goal := a.session.GoalState().Current
	return goal != nil && goal.ID == source.goalID && goal.Status == goals.Active && goal.Turns == source.turns
}

func (a *App) failGoalTurn(ctx context.Context, source *goalContinuation, err error) {
	if source == nil || err == nil {
		return
	}
	a.sessionLeaseMu.Lock()
	if !a.goalSourceMatchesLocked(source) {
		a.sessionLeaseMu.Unlock()
		return
	}
	state := a.session.GoalState()
	a.stopGoalTimer()
	if observation := goalObservation(ctx); observation != nil && observation.source.goalID == source.goalID && observation.source.turns == source.turns {
		input, output := observation.input, observation.output
		workerInput, workerOutput := goalWorkerUsage(ctx, observation.worker)
		input += workerInput
		output += workerOutput
		state.Current.InputTokens += max(input, 0)
		state.Current.OutputTokens += max(output, 0)
	}
	reason := truncateRunesApp(err.Error(), 4000)
	notice := "Goal paused: " + reason
	if unrecoverableGoalError(err) {
		state = goals.Clear(state, reason, time.Now())
		notice = "Goal cleared after an unrecoverable error: " + reason + ". Run /goal again to continue"
	} else {
		state.Current.Suspended, state.Current.Reason = true, reason
		state.Current.RetryAt = time.Time{}
	}
	var retry *goalScheduledAction
	if state.Current.Status == goals.Active {
		var retryErr error
		retry, retryErr = a.prepareGoalRetryLocked(ctx, source, &state, err)
		if retryErr != nil {
			state.Current.Reason = truncateRunesApp(reason+"; automatic retry blocked: "+retryErr.Error(), 4000)
			notice = "Goal paused: " + state.Current.Reason
		}
	}
	if saveErr := a.saveGoalState(state); saveErr != nil {
		a.sessionLeaseMu.Unlock()
		a.recordHeadlessTerminalOutcome("persistence_failed", saveErr.Error())
		a.safeSendToProgram(ui.ErrorMsg(saveErr))
		return
	}
	a.goalMu.Lock()
	a.goalNext, a.goalWaiting = nil, nil
	a.goalMu.Unlock()
	a.replaceQueuedGoal(nil)
	if retry != nil {
		a.armGoalTimerLocked(retry)
		notice = goalRetryNotice(state, reason)
	}
	a.sessionLeaseMu.Unlock()
	a.journalEvent("goal_turn_failed", map[string]any{"goal_id": source.goalID, "status": state.Current.Status, "reason": reason})
	a.currentPresenter().Warning(notice)
	a.safeSendToProgram(ui.RuntimeStatusMsg{Status: a.GetUIRuntimeStatus()})
	a.safeSendToProgram(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: notice})
}
