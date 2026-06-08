package loops

import (
	"testing"
	"time"
)

func newSelfPacedLoop(start time.Time, floor int64) *Loop {
	return &Loop{
		ID: "l", Task: "t", Mode: ModeSelfPaced, Status: StatusRunning,
		CreatedAt: start, MinDelaySeconds: floor,
	}
}

// A self-paced loop honors the model's "next:" hint, but an absurd value must
// be capped so it can't push NextRunAt years out and silently disable the loop.
func TestLoop_SelfPacedDelay_CapsAbsurdHint(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newSelfPacedLoop(start, DefaultMinDelaySeconds)
	l.AppendIteration(Iteration{N: 1, StartedAt: start, Duration: time.Second, OK: true, NextHint: "next 99999h"})

	lastRun := start.Add(time.Second)
	wantCap := lastRun.Add(MaxSelfPacedDelaySeconds * time.Second)
	if !l.NextRunAt.Equal(wantCap) {
		t.Errorf("absurd hint must be capped to %v, got %v", wantCap, l.NextRunAt)
	}
}

func TestLoop_SelfPacedDelay_HonorsNormalHint(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newSelfPacedLoop(start, DefaultMinDelaySeconds)
	l.AppendIteration(Iteration{N: 1, StartedAt: start, OK: true, NextHint: "next 30m"})

	if want := start.Add(30 * time.Minute); !l.NextRunAt.Equal(want) {
		t.Errorf("30m hint not honored: got %v, want %v", l.NextRunAt, want)
	}
}

// A user-configured floor ABOVE the cap takes precedence — the cap only bounds
// the model hint, it must never pull the next run below an explicit floor.
func TestLoop_SelfPacedDelay_UserFloorAboveCapWins(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	floor := int64(48 * 60 * 60) // 48h, above the 24h cap
	l := newSelfPacedLoop(start, floor)
	l.AppendIteration(Iteration{N: 1, StartedAt: start, OK: true, NextHint: "next 50h"})

	if want := start.Add(time.Duration(floor) * time.Second); !l.NextRunAt.Equal(want) {
		t.Errorf("user floor above cap must win: got %v, want %v", l.NextRunAt, want)
	}
}

func TestLoop_SelfPacedDelay_NoHintUsesFloor(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	l := newSelfPacedLoop(start, DefaultMinDelaySeconds)
	l.AppendIteration(Iteration{N: 1, StartedAt: start, OK: true, NextHint: ""})

	if want := start.Add(DefaultMinDelaySeconds * time.Second); !l.NextRunAt.Equal(want) {
		t.Errorf("no hint should fall back to the floor: got %v, want %v", l.NextRunAt, want)
	}
}
