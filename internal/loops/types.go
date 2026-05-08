// Package loops implements the /loop autonomous workflow system.
//
// A "loop" is a recurring task description that the agent executes on a
// schedule (interval-based or self-paced). State persists to disk so loops
// survive restarts. Each iteration:
//
//  1. Loads accumulated history from the loop's state file.
//  2. Builds a prompt from the task description + recent iteration summaries.
//  3. Submits the prompt through the normal request pipeline.
//  4. Captures the agent's summary back into the loop state.
//  5. Optionally writes a structured summary to project MEMORY.md.
//
// The user controls loops via the /loop command:
//
//	/loop                       List active loops
//	/loop <task>                Start self-paced loop
//	/loop <interval> <task>     Start interval loop (e.g. /loop 30m <task>)
//	/loop status <id>           Show loop details + recent iterations
//	/loop stop <id>             Stop a running loop
//	/loop pause <id>            Pause (won't fire until /loop resume)
//	/loop resume <id>           Re-arm a paused loop
//	/loop now <id>              Fire iteration immediately
//	/loop remove <id>           Delete loop state file
//
// Conflicts: if the user is interacting (typing, agent processing) when a
// loop is due to fire, the runner queues the trigger and waits until the
// session goes idle. Loops never preempt foreground work.
//
// Persistence: loops live as ~/.gokin/loops/<id>.json. Auto-resume on
// startup picks up wherever the prior process left off.
package loops

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Status enumerates the lifecycle states of a loop.
type Status string

const (
	// StatusRunning: scheduler will fire the next iteration when due.
	StatusRunning Status = "running"
	// StatusPaused: present in state, but scheduler skips it. User
	// re-arms via /loop resume.
	StatusPaused Status = "paused"
	// StatusStopped: terminal state — scheduler won't fire it again.
	// User can /loop remove to delete the state file, or leave it for
	// historical reference.
	StatusStopped Status = "stopped"
	// StatusCompleted: max_iterations reached, terminal. Same handling
	// as Stopped from scheduler's perspective; distinct so /loop status
	// can show it as success.
	StatusCompleted Status = "completed"
)

// Mode picks between fixed-interval and self-paced scheduling.
type Mode string

const (
	// ModeInterval: scheduler fires every IntervalSeconds regardless
	// of what the iteration says. Cron-like predictability.
	ModeInterval Mode = "interval"
	// ModeSelfPaced: scheduler fires after MinDelaySeconds (default
	// 5 minutes) so we don't hot-loop. The agent itself can include
	// "next check in X" hints that the runner respects.
	ModeSelfPaced Mode = "self-paced"
)

// Loop is the persisted state for a single recurring task.
type Loop struct {
	// ID is the stable identifier — used in filenames and /loop subcommand
	// args. Generated as a short hex string when the loop is created.
	ID string `json:"id"`

	// Task is the user-supplied description of what each iteration should
	// accomplish. Injected verbatim as the first part of every iteration
	// prompt; the rest is the accumulated summary context.
	Task string `json:"task"`

	// Mode + IntervalSeconds determine when iterations fire.
	Mode             Mode  `json:"mode"`
	IntervalSeconds  int64 `json:"interval_seconds,omitempty"`
	MinDelaySeconds  int64 `json:"min_delay_seconds,omitempty"` // self-paced floor; default 300
	MaxIterations    int   `json:"max_iterations,omitempty"`     // 0 = unlimited
	UpdateMemory     bool  `json:"update_memory"`                // auto-write summaries to MEMORY.md

	// Status drives the scheduler. See Status constants for semantics.
	Status Status `json:"status"`

	// Timestamps for ordering and scheduler decisions.
	CreatedAt   time.Time `json:"created_at"`
	LastRunAt   time.Time `json:"last_run_at,omitempty"`
	NextRunAt   time.Time `json:"next_run_at,omitempty"`
	StoppedAt   time.Time `json:"stopped_at,omitempty"`

	// Iterations is the append-only log of what each iteration did.
	// Capped at MaxHistorySize entries — older entries drop off the
	// front so the file doesn't grow unbounded over a multi-day loop.
	Iterations []Iteration `json:"iterations,omitempty"`

	// IterationCount is the lifetime count, including dropped-off entries.
	IterationCount int `json:"iteration_count"`

	// SuccessCount and FailureCount are lifetime counters of completed
	// iterations partitioned by outcome. Used by /loop list to
	// distinguish a loop making real progress from one silently
	// failing every cycle. Pre-existing loop state files written
	// before these fields were added load with zeros — they'll
	// re-populate correctly from the next iteration onward.
	SuccessCount int `json:"success_count,omitempty"`
	FailureCount int `json:"failure_count,omitempty"`
}

// Iteration is the result of one execution of the loop's task.
type Iteration struct {
	N           int           `json:"n"`              // 1-based iteration number
	StartedAt   time.Time     `json:"started_at"`
	Duration    time.Duration `json:"duration"`        // wall-clock for this iteration
	Summary     string        `json:"summary"`         // 1-3 sentence summary of what happened
	OK          bool          `json:"ok"`              // false = iteration errored or model returned nothing useful
	TokensIn    int           `json:"tokens_in,omitempty"`
	TokensOut   int           `json:"tokens_out,omitempty"`
	NextHint    string        `json:"next_hint,omitempty"` // e.g. "wait 1h" — self-paced runner respects
}

// MaxHistorySize caps the per-loop iterations slice. Set well above the
// number a typical user will care to scroll through but small enough that
// a runaway loop doesn't bloat the state file.
const MaxHistorySize = 50

// DefaultMinDelaySeconds is the floor between self-paced iterations.
// Prevents hot-loop runaway if the model keeps requesting "check again
// soon" — at minimum we wait this long.
const DefaultMinDelaySeconds = 300 // 5 minutes

// AppendIteration adds a result to the loop's history, dropping the
// oldest entry if we'd exceed MaxHistorySize. Updates IterationCount
// (lifetime), LastRunAt, SuccessCount/FailureCount, and (for interval
// mode) the next run time.
//
// Caller is responsible for persisting the loop after calling this.
func (l *Loop) AppendIteration(it Iteration) {
	l.IterationCount++
	if it.OK {
		l.SuccessCount++
	} else {
		l.FailureCount++
	}
	l.Iterations = append(l.Iterations, it)
	if len(l.Iterations) > MaxHistorySize {
		// Drop oldest. Realloc fresh slice so the GC can free the old
		// backing array's head — for a 50-entry cap this is cheap.
		trimmed := make([]Iteration, MaxHistorySize)
		copy(trimmed, l.Iterations[len(l.Iterations)-MaxHistorySize:])
		l.Iterations = trimmed
	}
	l.LastRunAt = it.StartedAt.Add(it.Duration)

	// Compute NextRunAt based on mode.
	switch l.Mode {
	case ModeInterval:
		if l.IntervalSeconds > 0 {
			l.NextRunAt = l.LastRunAt.Add(time.Duration(l.IntervalSeconds) * time.Second)
		}
	case ModeSelfPaced:
		delay := l.MinDelaySeconds
		if delay <= 0 {
			delay = DefaultMinDelaySeconds
		}
		// If the iteration suggested a longer delay via NextHint, honor it.
		if hinted := parseNextHintSeconds(it.NextHint); hinted > delay {
			delay = hinted
		}
		l.NextRunAt = l.LastRunAt.Add(time.Duration(delay) * time.Second)
	}

	// Cap on max iterations? Mark completed.
	if l.MaxIterations > 0 && l.IterationCount >= l.MaxIterations {
		l.Status = StatusCompleted
		l.StoppedAt = l.LastRunAt
	}
}

// IsActive returns true if the loop is in a state where the scheduler
// should consider firing it.
func (l *Loop) IsActive() bool {
	return l.Status == StatusRunning
}

// IsDue returns true if the loop is active AND its NextRunAt is in the
// past (or unset, meaning "fire immediately when first picked up").
func (l *Loop) IsDue(now time.Time) bool {
	if !l.IsActive() {
		return false
	}
	if l.NextRunAt.IsZero() {
		return true
	}
	return !now.Before(l.NextRunAt)
}

// Validate checks that a loop is internally consistent. Called by storage
// on load and by Manager.Add on create — surfaces malformed state files
// as clear errors instead of letting them silently misbehave at runtime.
func (l *Loop) Validate() error {
	if strings.TrimSpace(l.ID) == "" {
		return fmt.Errorf("loop: id is empty")
	}
	if strings.TrimSpace(l.Task) == "" {
		return fmt.Errorf("loop %s: task is empty", l.ID)
	}
	switch l.Mode {
	case ModeInterval:
		if l.IntervalSeconds <= 0 {
			return fmt.Errorf("loop %s: interval mode needs interval_seconds > 0", l.ID)
		}
	case ModeSelfPaced:
		// MinDelaySeconds may be 0 — the AppendIteration path falls back
		// to DefaultMinDelaySeconds.
	default:
		return fmt.Errorf("loop %s: unknown mode %q", l.ID, l.Mode)
	}
	switch l.Status {
	case StatusRunning, StatusPaused, StatusStopped, StatusCompleted:
		// OK
	default:
		return fmt.Errorf("loop %s: unknown status %q", l.ID, l.Status)
	}
	return nil
}

// parseNextHintSeconds parses common "next check" hints from the iteration
// summary. Recognizes formats like "wait 30m", "next 1h", "in 5m". Returns
// 0 when the hint is absent or unparseable — caller treats 0 as "use the
// default min delay".
func parseNextHintSeconds(hint string) int64 {
	hint = strings.TrimSpace(strings.ToLower(hint))
	if hint == "" {
		return 0
	}
	// Strip common prefix words.
	for _, prefix := range []string{"wait", "next", "in", "after", "every"} {
		hint = strings.TrimPrefix(hint, prefix)
	}
	hint = strings.TrimSpace(hint)
	d, err := time.ParseDuration(hint)
	if err != nil {
		return 0
	}
	if d < 0 {
		return 0
	}
	return int64(d / time.Second)
}

// Marshal returns the canonical JSON encoding for this loop. Used by
// Storage; exposed so tests and /loop status formatting can use it too.
func (l *Loop) Marshal() ([]byte, error) {
	return json.MarshalIndent(l, "", "  ")
}

// Unmarshal parses the canonical JSON encoding. Validates the result so
// corrupt files are rejected at the boundary, not after some downstream
// nil-deref.
func Unmarshal(data []byte) (*Loop, error) {
	var l Loop
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("loop: parse json: %w", err)
	}
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return &l, nil
}
