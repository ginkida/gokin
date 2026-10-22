// Package goals keeps the session-scoped completion condition independently
// of recurring scheduled work. Only an evaluator verdict achieves a goal.
package goals

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Status string

const (
	Active          Status = "active"
	Achieved        Status = "achieved"
	Impossible      Status = "impossible"
	Cleared         Status = "cleared"
	NoProgressLimit        = 3
)

type Goal struct {
	ID              string    `json:"id"`
	Condition       string    `json:"condition"`
	Status          Status    `json:"status"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Turns           int       `json:"turns"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	Reason          string    `json:"reason,omitempty"`
	NoProgressTurns int       `json:"no_progress_turns,omitempty"`
	Suspended       bool      `json:"suspended,omitempty"`
	RetryAttempts   int       `json:"retry_attempts,omitempty"`
	RetryAt         time.Time `json:"retry_at,omitempty"`
	WaitingSince    time.Time `json:"waiting_since,omitempty"`
	NextCheckIn     time.Time `json:"next_check_in,omitempty"`
	CheckInBackoff  int       `json:"check_in_backoff,omitempty"`
	IdleCheckIns    int       `json:"idle_check_ins,omitempty"`
}

type State struct {
	Current      *Goal `json:"current,omitempty"`
	LastAchieved *Goal `json:"last_achieved,omitempty"`
}

func Clone(state State) State {
	if state.Current != nil {
		goal := *state.Current
		state.Current = &goal
	}
	if state.LastAchieved != nil {
		goal := *state.LastAchieved
		state.LastAchieved = &goal
	}
	return state
}

func Set(state State, condition string, now time.Time) (State, error) {
	condition = strings.TrimSpace(condition)
	if condition == "" || !utf8.ValidString(condition) || utf8.RuneCountInString(condition) > 4000 || strings.ContainsRune(condition, '\x00') {
		return state, fmt.Errorf("goal condition must contain 1–4000 characters")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return state, err
	}
	state = Clone(state)
	state.Current = &Goal{ID: hex.EncodeToString(nonce[:]), Condition: condition, Status: Active, StartedAt: now}
	return state, nil
}

func Clear(state State, reason string, now time.Time) State {
	state = Clone(state)
	if state.Current != nil && state.Current.Status == Active {
		state.Current.Status, state.Current.Reason, state.Current.FinishedAt = Cleared, reason, now
		state.Current.Suspended = false
		state.Current.RetryAt, state.Current.WaitingSince, state.Current.NextCheckIn = time.Time{}, time.Time{}, time.Time{}
	}
	return state
}

func Resume(state State, now time.Time) State {
	state = Clone(state)
	if goal := state.Current; goal != nil && goal.Status == Active {
		goal.StartedAt, goal.FinishedAt = now, time.Time{}
		goal.Turns, goal.InputTokens, goal.OutputTokens, goal.NoProgressTurns = 0, 0, 0, 0
		goal.Reason, goal.Suspended = "", false
		goal.RetryAttempts, goal.CheckInBackoff, goal.IdleCheckIns = 0, 0, 0
		goal.RetryAt, goal.WaitingSince, goal.NextCheckIn = time.Time{}, time.Time{}, time.Time{}
	}
	return state
}

func Apply(state State, verdict Verdict, usedTools bool, input, output int, now time.Time) (State, error) {
	if err := verdict.Validate(); err != nil {
		return state, err
	}
	state = Clone(state)
	goal := state.Current
	if goal == nil || goal.Status != Active {
		return state, fmt.Errorf("no active goal")
	}
	goal.Turns++
	goal.InputTokens += max(input, 0)
	goal.OutputTokens += max(output, 0)
	goal.Reason, goal.Suspended = verdict.Reason, false
	goal.RetryAttempts = 0
	goal.RetryAt, goal.WaitingSince, goal.NextCheckIn = time.Time{}, time.Time{}, time.Time{}
	if usedTools {
		goal.NoProgressTurns = 0
	} else {
		goal.NoProgressTurns++
	}
	switch verdict.Verdict {
	case "met":
		goal.Status, goal.FinishedAt = Achieved, now
		copy := *goal
		state.LastAchieved = &copy
	case "impossible":
		goal.Status, goal.FinishedAt = Impossible, now
	case "not_met":
		if goal.NoProgressTurns >= NoProgressLimit {
			goal.Suspended = true
			goal.Reason = "No tool use for several goal turns. " + verdict.Reason
		}
	}
	return state, nil
}

type Verdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

func (v Verdict) Validate() error {
	if v.Verdict != "met" && v.Verdict != "not_met" && v.Verdict != "impossible" {
		return fmt.Errorf("invalid goal verdict %q", v.Verdict)
	}
	if strings.TrimSpace(v.Reason) == "" || utf8.RuneCountInString(v.Reason) > 4000 {
		return fmt.Errorf("goal verdict must include a reason of at most 4000 characters")
	}
	return nil
}

func Validate(state State) error {
	for _, goal := range []*Goal{state.Current, state.LastAchieved} {
		if goal == nil {
			continue
		}
		if len(goal.ID) != 32 {
			return fmt.Errorf("invalid goal identity")
		}
		if _, err := hex.DecodeString(goal.ID); err != nil {
			return fmt.Errorf("invalid goal identity")
		}
		if strings.TrimSpace(goal.Condition) == "" || utf8.RuneCountInString(goal.Condition) > 4000 || !utf8.ValidString(goal.Condition) || strings.ContainsRune(goal.Condition, '\x00') {
			return fmt.Errorf("invalid saved goal condition")
		}
		if goal.Status != Active && goal.Status != Achieved && goal.Status != Impossible && goal.Status != Cleared {
			return fmt.Errorf("invalid goal status")
		}
		if goal.StartedAt.IsZero() || goal.Turns < 0 || goal.InputTokens < 0 || goal.OutputTokens < 0 || goal.NoProgressTurns < 0 || utf8.RuneCountInString(goal.Reason) > 5000 {
			return fmt.Errorf("invalid goal accounting")
		}
		if goal.RetryAttempts < 0 || goal.RetryAttempts > RetryLimit || goal.IdleCheckIns < 0 || goal.IdleCheckIns > IdleCheckInLimit || goal.CheckInBackoff < 0 || goal.CheckInBackoff > 2 || !goal.NextCheckIn.IsZero() && goal.WaitingSince.IsZero() || !goal.RetryAt.IsZero() && (goal.Status != Active || !goal.Suspended || goal.RetryAttempts == 0) {
			return fmt.Errorf("invalid goal schedule")
		}
	}
	if state.LastAchieved != nil && state.LastAchieved.Status != Achieved {
		return fmt.Errorf("invalid last achieved goal")
	}
	return nil
}

func InitialPrompt(condition string) string {
	return "Work toward this goal and verify its complete condition with evidence:\n\n" + condition
}
func ContinuationPrompt(goal *Goal) string {
	return "Continue working toward the active goal. Preserve completed work and use tools to make or verify progress. Do not treat a partial milestone as completion.\n\nCondition:\n" + goal.Condition + "\n\nIndependent evaluator's reason:\n" + goal.Reason
}
