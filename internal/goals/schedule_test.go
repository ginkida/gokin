package goals

import (
	"testing"
	"time"
)

func TestGoalCheckInIntervalConfiguration(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": 30 * time.Minute, "0": 0, "0.01": 600 * time.Millisecond, " 2 ": 2 * time.Minute, "-1": 30 * time.Minute, "NaN": 30 * time.Minute, "Inf": 30 * time.Minute, "1e300": 30 * time.Minute, "invalid": 30 * time.Minute} {
		if got := ParseCheckInInterval(raw); got != want {
			t.Errorf("%q: %s, want %s", raw, got, want)
		}
	}
	for i, want := range []time.Duration{15 * time.Second, 30 * time.Second, 60 * time.Second, 60 * time.Second} {
		if got := RetryDelay(i + 1); got != want {
			t.Errorf("retry %d: %s", i+1, got)
		}
	}
}

func TestGoalCheckInsBackOffAndCapOnlyIdleDelivery(t *testing.T) {
	now := time.Now()
	state, _ := Set(State{}, "complete the entire condition", now)
	goal := state.Current
	DeferBackground(goal, now, time.Minute)
	if CheckInDue(goal, now.Add(59*time.Second)) || !CheckInDue(goal, now.Add(time.Minute)) {
		t.Fatal("wrong initial check-in deadline")
	}
	for i, interval := range []time.Duration{2 * time.Minute, 4 * time.Minute, 4 * time.Minute} {
		now = goal.NextCheckIn
		if !RecordCheckIn(goal, now, time.Minute, true) || goal.IdleCheckIns != i+1 || goal.NextCheckIn.Sub(now) != interval {
			t.Fatalf("wrong capped backoff: %+v", goal)
		}
	}
	now = goal.NextCheckIn
	if RecordCheckIn(goal, now, time.Minute, true) {
		t.Fatal("fourth idle check-in was allowed")
	}
	if !RecordCheckIn(goal, now, time.Minute, false) || goal.IdleCheckIns != 3 {
		t.Fatal("idle cap suppressed a due turn-end check-in")
	}
	goal.RetryAttempts, goal.RetryAt = 2, now.Add(time.Second)
	goal.Suspended = true
	if err := Validate(state); err != nil {
		t.Fatal(err)
	}
	resumed := Resume(state, now)
	if resumed.Current.RetryAttempts != 0 || resumed.Current.IdleCheckIns != 0 || !resumed.Current.NextCheckIn.IsZero() || !resumed.Current.RetryAt.IsZero() || resumed.Current.CheckInBackoff != 0 {
		t.Fatal("resume did not reset the schedule baseline")
	}
	DeferBackground(goal, now, 0)
	if !goal.NextCheckIn.IsZero() {
		t.Fatal("zero did not disable check-ins")
	}
}

func TestGoalRejectsInvalidSavedSchedules(t *testing.T) {
	for _, change := range []func(*Goal){func(g *Goal) { g.IdleCheckIns = 4 }, func(g *Goal) { g.RetryAttempts = 4 }, func(g *Goal) { g.CheckInBackoff = 3 }, func(g *Goal) { g.NextCheckIn = time.Now() }, func(g *Goal) { g.RetryAt = time.Now() }} {
		state, _ := Set(State{}, "condition", time.Now())
		change(state.Current)
		if err := Validate(state); err == nil {
			t.Fatal("invalid schedule accepted")
		}
	}
}
