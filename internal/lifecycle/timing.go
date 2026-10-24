// Package lifecycle measures CLI startup, headless turns and shutdown without
// recording prompts, commands, credentials or model output.
package lifecycle

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type Timing struct {
	StartupMS      int64            `json:"startup_ms"`
	RunMS          int64            `json:"run_ms"`
	ShutdownMS     *int64           `json:"shutdown_ms,omitempty"`
	BetweenTurnsMS int64            `json:"between_turns_ms,omitempty"`
	PhasesMS       map[string]int64 `json:"phases_ms,omitempty"`
}

type Trace struct {
	mu     sync.Mutex
	writer io.Writer

	started      time.Time
	startupEnd   time.Time
	turnStart    time.Time
	turnEnd      time.Time
	finished     time.Time
	run, between time.Duration

	group, phase           string
	groupStart, phaseStart time.Time
	phases, groupPhases    map[string]time.Duration
	timer                  *time.Timer
	warningAfter           time.Duration
	warned                 bool
	generation             uint64
}

func New(writer io.Writer) *Trace { return newTrace(writer, 10*time.Second) }

func newTrace(writer io.Writer, warningAfter time.Duration) *Trace {
	if writer == nil {
		writer = io.Discard
	}
	now := time.Now()
	t := &Trace{writer: writer, started: now, phases: map[string]time.Duration{}, warningAfter: warningAfter}
	t.groupLocked("startup", "cli", now)
	return t
}

// Phase names must be static labels, never caller input.
func (t *Trace) Phase(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.finishPhaseLocked(now)
	t.phase, t.phaseStart = name, now
}

// IncludeLaunch accounts for the detached launch before this process entered
// runApp. Its timestamp comes from the durable job, never an inferred delay.
func (t *Trace) IncludeLaunch(startedAt time.Time) {
	if t == nil || startedAt.IsZero() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.startupEnd.IsZero() || !startedAt.Before(t.started) {
		return
	}
	gap := t.started.Sub(startedAt)
	t.started, t.groupStart = startedAt, startedAt
	t.phases["startup.worker_launch"] += gap
	t.groupPhases["worker_launch"] += gap
	t.warnLocked(time.Now())
	t.armWarningLocked()
}

func (t *Trace) finishPhaseLocked(now time.Time) {
	if t.phase == "" {
		return
	}
	d := now.Sub(t.phaseStart)
	t.phases[t.group+"."+t.phase] += d
	t.groupPhases[t.phase] += d
	t.phase = ""
}

func (t *Trace) groupLocked(group, phase string, now time.Time) {
	if t.timer != nil {
		t.timer.Stop()
	}
	t.group, t.phase, t.groupStart, t.phaseStart = group, phase, now, now
	t.groupPhases = map[string]time.Duration{}
	t.warned = false
	t.armWarningLocked()
}

func (t *Trace) armWarningLocked() {
	if t.timer != nil {
		t.timer.Stop()
	}
	t.generation++
	if t.group == "run" || t.warned {
		return
	}
	generation := t.generation
	timerDelay := max(time.Duration(0), t.warningAfter-time.Since(t.groupStart))
	t.timer = time.AfterFunc(timerDelay, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.generation == generation && t.finished.IsZero() {
			t.warnLocked(time.Now())
		}
	})
}

func (t *Trace) warnLocked(now time.Time) {
	if t.warned || now.Sub(t.groupStart) < t.warningAfter {
		return
	}
	t.warned = true
	slowest, duration := t.phase, now.Sub(t.phaseStart)
	for name, d := range t.groupPhases {
		if d > duration {
			slowest, duration = name, d
		}
	}
	fmt.Fprintf(t.writer, "Warning: headless %s exceeded %s; slowest phase=%s duration=%s elapsed=%s.\n", t.group, t.warningAfter, slowest, duration.Round(time.Millisecond), now.Sub(t.groupStart).Round(time.Millisecond))
}

func (t *Trace) StartTurn() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.warnLocked(now)
	t.finishPhaseLocked(now)
	if t.startupEnd.IsZero() {
		t.startupEnd = now
	} else if !t.turnEnd.IsZero() {
		t.between += now.Sub(t.turnEnd)
	}
	t.turnStart = now
	t.groupLocked("run", "headless", now)
}

func (t *Trace) EndTurn() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.turnStart.IsZero() {
		return
	}
	now := time.Now()
	t.finishPhaseLocked(now)
	t.run += now.Sub(t.turnStart)
	t.turnStart, t.turnEnd = time.Time{}, now
	t.groupLocked("shutdown", "background_control", now)
}

func (t *Trace) Finish() Timing {
	if t == nil {
		return Timing{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished.IsZero() {
		now := time.Now()
		t.warnLocked(now)
		t.finishPhaseLocked(now)
		t.finished = now
		if t.timer != nil {
			t.timer.Stop()
		}
	}
	return t.snapshotLocked(t.finished)
}

func (t *Trace) Snapshot() Timing {
	if t == nil {
		return Timing{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.finished
	if now.IsZero() {
		now = time.Now()
	}
	return t.snapshotLocked(now)
}

func (t *Trace) snapshotLocked(now time.Time) Timing {
	startupEnd := t.startupEnd
	if startupEnd.IsZero() {
		startupEnd = now
	}
	run := t.run
	if !t.turnStart.IsZero() {
		run += now.Sub(t.turnStart)
	}
	snapshot := Timing{StartupMS: startupEnd.Sub(t.started).Milliseconds(), RunMS: run.Milliseconds(), BetweenTurnsMS: t.between.Milliseconds(), PhasesMS: map[string]int64{}}
	for name, d := range t.phases {
		snapshot.PhasesMS[name] = d.Milliseconds()
	}
	if t.phase != "" {
		snapshot.PhasesMS[t.group+"."+t.phase] += now.Sub(t.phaseStart).Milliseconds()
	}
	if !t.finished.IsZero() && !t.turnEnd.IsZero() {
		d := now.Sub(t.turnEnd).Milliseconds()
		snapshot.ShutdownMS = &d
	}
	return snapshot
}
