package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/client"
	"gokin/internal/goals"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

type goalScheduledAction struct {
	source     goalContinuation
	kind       string
	due        time.Time
	timer      *time.Timer
	stopParent func() bool
}

func goalCheckInInterval() time.Duration {
	raw, set := os.LookupEnv("GOKIN_GOAL_CHECKIN_MINUTES")
	if !set {
		raw = os.Getenv("CLAUDE_CODE_GOAL_CHECKIN_MINUTES")
	}
	return goals.ParseCheckInInterval(raw)
}

func retryableGoalError(err error) bool {
	if err == nil || isAutomaticRetryUnsafe(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || tools.IsHookStopError(err) || client.IsTerminalProviderError(err) || client.IsRateLimitError(err) || unrecoverableGoalError(err) || errors.Is(err, ErrRequestCircuitOpen) {
		return false
	}
	return client.IsOverloadError(err) || client.IsRetryableError(err) || client.IsHTTPTimeout(err) || client.IsStreamIdleTimeout(err)
}

func (a *App) stopGoalTimer() {
	a.goalMu.Lock()
	defer a.goalMu.Unlock()
	a.stopGoalTimerLocked()
}

func (a *App) stopGoalTimerLocked() {
	if action := a.goalTimer; action != nil {
		a.goalTimer = nil
		if action.timer != nil {
			action.timer.Stop()
		}
		if action.stopParent != nil {
			action.stopParent()
		}
	}
}

// Caller holds the conversation lease. Every timer is bound to an intent and
// conversation generation; an idle timer never owns a provider request.
func (a *App) armGoalTimerLocked(action *goalScheduledAction) {
	a.stopGoalTimer()
	a.mu.Lock()
	blocked := a.shuttingDown || a.dropSteerLeftovers || a.headlessRunActive || a.headlessDirect
	a.mu.Unlock()
	if blocked || goalCheckInInterval() == 0 || a.ctx != nil && a.ctx.Err() != nil {
		return
	}
	a.goalMu.Lock()
	a.goalTimer = action
	a.setGoalTimerDelayLocked(action, max(time.Until(action.due), 0))
	if a.ctx != nil {
		action.stopParent = context.AfterFunc(a.ctx, func() {
			a.goalMu.Lock()
			if a.goalTimer == action {
				a.stopGoalTimerLocked()
			}
			a.goalMu.Unlock()
		})
	}
	a.goalMu.Unlock()
}

func (a *App) setGoalTimerDelayLocked(action *goalScheduledAction, delay time.Duration) {
	action.timer = time.AfterFunc(delay, func() {
		if !a.foregroundWorkers.Add() {
			return
		}
		defer a.foregroundWorkers.Done()
		a.fireGoalAction(action)
	})
}

func (a *App) goalActionIsCurrent(action *goalScheduledAction) bool {
	a.goalMu.Lock()
	defer a.goalMu.Unlock()
	return a.goalTimer == action
}

func (a *App) fireGoalAction(action *goalScheduledAction) {
	a.sessionLeaseMu.Lock()
	if !a.goalActionIsCurrent(action) {
		a.sessionLeaseMu.Unlock()
		return
	}
	if !a.goalSourceMatchesLocked(&action.source) || goalCheckInInterval() == 0 {
		a.stopGoalTimer()
		a.sessionLeaseMu.Unlock()
		return
	}
	a.mu.Lock()
	blocked := a.shuttingDown || a.dropSteerLeftovers || a.headlessRunActive || a.headlessDirect || a.ctx != nil && a.ctx.Err() != nil
	busy := a.processing || a.pendingCount() > 0
	a.mu.Unlock()
	if blocked {
		a.stopGoalTimer()
		a.sessionLeaseMu.Unlock()
		return
	}
	if busy {
		a.goalMu.Lock()
		if a.goalTimer == action {
			a.setGoalTimerDelayLocked(action, time.Second)
		}
		a.goalMu.Unlock()
		a.sessionLeaseMu.Unlock()
		return
	}
	if err := a.goalPolicyError(); err != nil {
		a.stopGoalTimer()
		a.sessionLeaseMu.Unlock()
		a.failGoalTurn(context.Background(), &action.source, err)
		return
	}
	state := a.session.GoalState()
	next := action.source
	if action.kind == "checkin" {
		if !goals.RecordCheckIn(state.Current, time.Now(), goalCheckInInterval(), true) {
			a.stopGoalTimer()
			a.sessionLeaseMu.Unlock()
			return
		}
		next.prompt = a.goalCheckInPrompt(state.Current, true)
		next.checkIn = true
		next.generated = true
		state.Current.Reason = "Checking background work toward the goal"
	} else {
		if state.Current.RetryAt.IsZero() || !state.Current.RetryAt.Equal(action.due) {
			a.stopGoalTimer()
			a.sessionLeaseMu.Unlock()
			return
		}
		state.Current.RetryAt = time.Time{}
		state.Current.Reason = "Retrying after a temporary failure"
	}
	state.Current.Suspended = false
	if err := a.saveGoalState(state); err != nil {
		a.stopGoalTimer()
		a.sessionLeaseMu.Unlock()
		a.safeSendToProgram(ui.ErrorMsg(err))
		return
	}
	a.stopGoalTimer()
	a.goalMu.Lock()
	a.goalWaiting = nil
	a.goalMu.Unlock()
	launch := a.queueGoalActionLocked(&next)
	a.sessionLeaseMu.Unlock()
	if action.kind == "checkin" && state.Current.IdleCheckIns >= goals.IdleCheckInLimit {
		notice := "Idle goal check-ins are paused until you send another prompt"
		a.currentPresenter().Warning(notice)
		a.safeSendToProgram(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: notice})
	}
	if launch != nil {
		a.startAcceptedSubmit(launch, next.prompt, "", nil, false)
	}
}

// Caller holds sessionLeaseMu. Explicit input keeps its FIFO priority.
func (a *App) queueGoalActionLocked(next *goalContinuation) context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.shuttingDown || a.dropSteerLeftovers || a.headlessRunActive || a.headlessDirect {
		return nil
	}
	if a.processing {
		a.replaceQueuedGoal(next)
		return nil
	}
	if a.pendingCount() > 0 {
		return nil
	}
	a.processing = true
	ctx := a.claimForegroundContextLocked()
	ctx = withConversationLineage(ctx, next.sessionID, next.epoch)
	return context.WithValue(ctx, goalContinuationKey{}, next)
}

func (a *App) goalCheckInPrompt(goal *goals.Goal, idle bool) string {
	ids := []string{}
	for key := range a.runningGoalTasks() {
		ids = append(ids, key)
	}
	sort.Strings(ids)
	tasks := make([]map[string]string, 0, len(ids))
	for _, key := range ids {
		kind, id, _ := strings.Cut(key, ":")
		tasks = append(tasks, map[string]string{"type": kind, "id": id})
	}
	encoded, _ := json.Marshal(tasks)
	prompt := goals.ContinuationPrompt(goal) + "\n\nBackground work check-in. Inspect retained task output using task_output. Keep waiting if tasks are progressing; fix or stop tasks that are stuck. Preserve completed mutations. If the tasks have stopped without reporting a result, inspect their output and continue toward the goal.\nCurrent running tasks (data): " + string(encoded)
	if idle && goal.IdleCheckIns >= goals.IdleCheckInLimit {
		prompt += "\nThis is the third idle check-in. Idle check-ins are paused until the user sends another prompt."
	}
	return prompt
}

func (a *App) prepareGoalRetryLocked(ctx context.Context, source *goalContinuation, state *goals.State, err error) (*goalScheduledAction, error) {
	a.mu.Lock()
	blocked := a.shuttingDown || a.dropSteerLeftovers || a.headlessRunActive || a.headlessDirect
	a.mu.Unlock()
	observation := goalObservation(ctx)
	if blocked || observation == nil || !retryableGoalError(err) || goalCheckInInterval() == 0 || state.Current.RetryAttempts >= goals.RetryLimit || source.intent != a.goalIntent.Load() || ctx.Err() != nil {
		return nil, nil
	}
	next := *source
	next.scheduled, next.intent = true, a.goalIntent.Load()
	next.generated = true
	next.evaluateOnly = observation.evaluating
	next.prompt = observation.message
	if next.prompt == "" {
		next.prompt = goals.ContinuationPrompt(state.Current)
	}
	if !next.evaluateOnly {
		entries := a.sideEffectRecoverySnapshot()
		serialized, serializeErr := serializeToolCheckpointsExact(entries)
		if serializeErr != nil {
			return nil, serializeErr
		}
		// Journal entries still reference the executed call's argument maps.
		// Freeze an owned generation before a later turn can mutate those maps;
		// preserve integers above 2^53 while decoding the JSON snapshot.
		encoded, encodeErr := json.Marshal(serialized)
		if encodeErr != nil {
			return nil, encodeErr
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var owned []chat.SerializedToolCheckpoint
		if decodeErr := decoder.Decode(&owned); decodeErr != nil {
			return nil, decodeErr
		}
		serialized = owned
		// Use the same exact-outcome validation as durable request recovery.
		probe := chat.SerializedPendingRecovery{ID: "goal-retry", SessionID: source.sessionID, Message: next.prompt, Kind: "auto_resume", Attempt: 1, State: chat.PendingRecoveryScheduled, Checkpoints: serialized}
		probe.GenerationSignature = chat.PendingRecoveryGenerationSignature(probe)
		if validateErr := validatePendingRecovery(probe); validateErr != nil {
			return nil, validateErr
		}
		next.checkpoints = deserializeToolCheckpoints(serialized)
		a.session.SetToolCheckpoints(serialized)
	}
	state.Current.RetryAttempts++
	state.Current.RetryAt = time.Now().Add(goals.RetryDelay(state.Current.RetryAttempts))
	return &goalScheduledAction{kind: "retry", source: next, due: state.Current.RetryAt}, nil
}

// Called at user admission, before another callback can claim the foreground.
func (a *App) invalidateGoalUserInputLocked(message string) {
	if strings.TrimSpace(message) == "" || strings.HasPrefix(strings.TrimSpace(message), "/") {
		return
	}
	a.goalIntent.Add(1)
	a.stopGoalTimer()
	a.replaceQueuedGoal(nil)
}

func goalRetryNotice(state goals.State, reason string) string {
	return fmt.Sprintf("Goal still active: %s. Retrying in %s (%d/%d)", reason, time.Until(state.Current.RetryAt).Round(time.Second), state.Current.RetryAttempts, goals.RetryLimit)
}
