package app

import (
	"testing"
	"time"

	"gokin/internal/loops"
)

func TestDescribeLoopNext_TerminalStates(t *testing.T) {
	cases := []struct {
		name   string
		status loops.Status
		auto   bool
		want   string
	}{
		{"completed", loops.StatusCompleted, false, "completed"},
		{"stopped", loops.StatusStopped, false, "stopped"},
		{"auto-paused", loops.StatusPaused, true, "auto-paused"},
		{"manually-paused", loops.StatusPaused, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := &loops.Loop{Status: c.status, AutoPaused: c.auto}
			got := describeLoopNext(l)
			if got != c.want {
				t.Errorf("describeLoopNext(%s, autoPaused=%v) = %q, want %q",
					c.status, c.auto, got, c.want)
			}
		})
	}
}

func TestDescribeLoopNext_RunningWithFutureNextRun(t *testing.T) {
	l := &loops.Loop{
		Status:    loops.StatusRunning,
		NextRunAt: time.Now().Add(5 * time.Minute),
	}
	got := describeLoopNext(l)
	// Allow either "next in 5m" or "next in 4m" depending on rounding.
	if got != "next in 5m" && got != "next in 4m" {
		t.Errorf("describeLoopNext running+5m = %q, want next-in-5m-ish", got)
	}
}

func TestDescribeLoopNext_RunningWithPastNextRun(t *testing.T) {
	l := &loops.Loop{
		Status:    loops.StatusRunning,
		NextRunAt: time.Now().Add(-1 * time.Minute), // overdue
	}
	got := describeLoopNext(l)
	if got != "next now" {
		t.Errorf("describeLoopNext running+overdue = %q, want %q", got, "next now")
	}
}

func TestDescribeLoopNext_RunningWithZeroNextRun(t *testing.T) {
	l := &loops.Loop{Status: loops.StatusRunning} // NextRunAt zero
	got := describeLoopNext(l)
	if got != "" {
		t.Errorf("describeLoopNext running+zero-NextRunAt = %q, want empty", got)
	}
}

func TestFormatLoopDurationShort(t *testing.T) {
	cases := []struct {
		dur  time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{1 * time.Minute, "1m"},
		{45 * time.Minute, "45m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h"},
		{1*time.Hour + 30*time.Minute, "1h30m"},
		{23 * time.Hour, "23h"},
		{23*time.Hour + 59*time.Minute, "23h59m"},
		{24 * time.Hour, "1d"},
		{3 * 24 * time.Hour, "3d"},
	}
	for _, c := range cases {
		got := formatLoopDurationShort(c.dur)
		if got != c.want {
			t.Errorf("formatLoopDurationShort(%v) = %q, want %q", c.dur, got, c.want)
		}
	}
}
