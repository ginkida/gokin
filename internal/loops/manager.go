package loops

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
)

// ErrLoopGone signals that a loop the caller wanted to mutate has been
// removed (or never existed). Returned by RecordIteration when the
// loop disappears between the scheduler's snapshot and the post-spawn
// record call. Distinct from "loop is paused/stopped" — those return
// ErrLoopNotRunning so callers can react differently (e.g. skip
// downstream callbacks entirely on Gone, log + skip-side-effects on
// NotRunning).
var ErrLoopGone = errors.New("loop: removed")

// ErrLoopNotRunning signals that the loop exists but its status is
// not Running — e.g. user paused/stopped it during a long iteration.
// Returned by RecordIteration so the runner skips downstream side
// effects (markdown writes, notifications) for state the user has
// explicitly walked away from.
var ErrLoopNotRunning = errors.New("loop: not running")

// Manager is the concurrency-safe container for active loops. The TUI,
// the /loop command, and the background scheduler all interact with
// loops through this single object — never by reaching into Storage
// directly. That keeps the in-memory state and on-disk state in sync.
//
// Locking discipline: every method that mutates a loop either copies the
// loop under m.mu (for read-only consumers) or holds m.mu for the full
// mutate-and-save path. Storage.Save is called UNDER m.mu — the disk
// path is fast and we don't want a Save in flight while another caller
// modifies the same Loop object out from under it.
type Manager struct {
	storage Storage

	mu    sync.RWMutex
	loops map[string]*Loop

	// onRemove fires after a successful Remove so external state (e.g.
	// the markdown memory file) can be cleaned up too. Optional;
	// nil-safe. Wired by the App constructor — keeps Manager unaware
	// of UI/memory layers (clean dependency direction: Manager →
	// Storage only).
	onRemove func(loopID string)
}

// SetOnRemove installs a callback fired after each successful Remove.
// Idempotent — second call replaces the prior callback. Safe to call
// from any goroutine.
func (m *Manager) SetOnRemove(fn func(loopID string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onRemove = fn
}

// NewManager constructs a Manager backed by the given Storage. Loads
// any existing loop state files immediately so /loop list shows them
// from the first call, even if no /loop add has happened this session.
//
// Load errors are logged at Warn (not returned) — a corrupt loop file
// shouldn't block the rest from loading. Users see the error in
// gokin.log and can /loop remove the bad ID or fix the file by hand.
func NewManager(storage Storage) *Manager {
	m := &Manager{
		storage: storage,
		loops:   make(map[string]*Loop),
	}

	loaded, errs := storage.Load()
	for _, l := range loaded {
		m.loops[l.ID] = l
	}
	for _, err := range errs {
		logging.Warn("loops: skipped corrupt state file", "error", err)
	}
	if len(loaded) > 0 {
		logging.Info("loops: loaded state files", "count", len(loaded))
	}

	return m
}

// Add creates a new loop, persists it, and returns it. The caller
// supplies task + mode + interval; everything else is initialized to
// reasonable defaults (running, no iterations, computed NextRunAt).
//
// Returns an error only on validation failure or storage failure —
// never silently mutates partial state on disk.
func (m *Manager) Add(task string, mode Mode, intervalSeconds int64, opts ...AddOption) (*Loop, error) {
	now := time.Now()
	l := &Loop{
		ID:               NewID(),
		Task:             strings.TrimSpace(task),
		Mode:             mode,
		IntervalSeconds:  intervalSeconds,
		MinDelaySeconds:  DefaultMinDelaySeconds,
		Status:           StatusRunning,
		CreatedAt:        now,
		UpdateMemory:     true, // default per user-confirmed design
	}
	for _, opt := range opts {
		opt(l)
	}

	// First fire: interval mode waits IntervalSeconds; self-paced fires
	// immediately on the next scheduler tick.
	if mode == ModeInterval {
		l.NextRunAt = now.Add(time.Duration(intervalSeconds) * time.Second)
	} else {
		l.NextRunAt = now
	}

	if err := l.Validate(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.storage.Save(l); err != nil {
		return nil, err
	}
	m.loops[l.ID] = l
	logging.Info("loops: added", "id", l.ID, "mode", l.Mode, "task_preview", previewTask(l.Task))
	return l, nil
}

// AddOption tunes a new loop at construction. Used for less-common
// fields that don't fit the common Add() signature.
type AddOption func(*Loop)

// WithMaxIterations caps the lifetime iteration count. 0 = unlimited
// (the default).
func WithMaxIterations(n int) AddOption {
	return func(l *Loop) { l.MaxIterations = n }
}

// WithMinDelay overrides the self-paced floor. Ignored for interval
// mode (which uses IntervalSeconds directly).
func WithMinDelay(seconds int64) AddOption {
	return func(l *Loop) { l.MinDelaySeconds = seconds }
}

// WithoutMemory disables auto-write of iteration summaries to MEMORY.md.
// Default is enabled (per user-confirmed design).
func WithoutMemory() AddOption {
	return func(l *Loop) { l.UpdateMemory = false }
}

// Get returns a snapshot of one loop by ID. Returns (nil, false) if not
// present. The returned pointer is a deep copy — safe to read without
// further locking, but mutations don't propagate to the manager.
func (m *Manager) Get(id string) (*Loop, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.loops[id]
	if !ok {
		return nil, false
	}
	return cloneLoop(l), true
}

// List returns snapshots of all loops, sorted by CreatedAt (oldest first)
// then ID. Same ordering as Storage.Load — stable across runs.
func (m *Manager) List() []*Loop {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Loop, 0, len(m.loops))
	for _, l := range m.loops {
		out = append(out, cloneLoop(l))
	}
	sortLoopsForDisplay(out)
	return out
}

// Active returns snapshots of running loops only — convenience for
// the scheduler and /loop default listing (which hides stopped loops).
func (m *Manager) Active() []*Loop {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Loop, 0, len(m.loops))
	for _, l := range m.loops {
		if l.IsActive() {
			out = append(out, cloneLoop(l))
		}
	}
	sortLoopsForDisplay(out)
	return out
}

// Stop marks a loop as terminal. Safe to call on already-stopped loops
// (no-op). Persists to disk so the change survives restart.
func (m *Manager) Stop(id string) error {
	return m.transition(id, func(l *Loop) error {
		if l.Status == StatusStopped || l.Status == StatusCompleted {
			return nil
		}
		l.Status = StatusStopped
		l.StoppedAt = time.Now()
		return nil
	})
}

// Pause moves a running loop to paused. Returns error if loop isn't
// running (paused/stopped/completed loops can't pause again).
func (m *Manager) Pause(id string) error {
	return m.transition(id, func(l *Loop) error {
		if l.Status != StatusRunning {
			return fmt.Errorf("loop %s: cannot pause from status %s", id, l.Status)
		}
		l.Status = StatusPaused
		return nil
	})
}

// Resume re-arms a paused loop. Recomputes NextRunAt from "now" so a
// loop paused for hours doesn't fire immediately N times to "catch up".
func (m *Manager) Resume(id string) error {
	return m.transition(id, func(l *Loop) error {
		if l.Status != StatusPaused {
			return fmt.Errorf("loop %s: cannot resume from status %s", id, l.Status)
		}
		l.Status = StatusRunning
		now := time.Now()
		if l.Mode == ModeInterval {
			l.NextRunAt = now.Add(time.Duration(l.IntervalSeconds) * time.Second)
		} else {
			l.NextRunAt = now
		}
		return nil
	})
}

// FireNow forces NextRunAt to "now" — the next scheduler tick will pick
// up the loop. Returns error if the loop isn't running.
func (m *Manager) FireNow(id string) error {
	return m.transition(id, func(l *Loop) error {
		if l.Status != StatusRunning {
			return fmt.Errorf("loop %s: cannot fire from status %s", id, l.Status)
		}
		l.NextRunAt = time.Now()
		return nil
	})
}

// Remove deletes a loop both in-memory and on disk. Distinct from Stop:
// after Remove the loop is gone — no historical reference, no /loop
// status. Use Stop for "I don't want this to fire anymore but keep the
// record"; use Remove for "purge this entirely".
//
// Fires onRemove (if set) after the manager-side delete succeeds so
// downstream stores (per-loop markdown, etc.) can clean up too.
// Callback runs OUTSIDE m.mu to avoid lock-order issues with whatever
// the callback touches.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	if _, ok := m.loops[id]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("loop %s: not found", id)
	}
	if err := m.storage.Delete(id); err != nil {
		m.mu.Unlock()
		return err
	}
	delete(m.loops, id)
	cb := m.onRemove
	m.mu.Unlock()
	logging.Info("loops: removed", "id", id)
	if cb != nil {
		cb(id)
	}
	return nil
}

// RecordIteration appends an iteration to the loop and persists. Called
// by the runner after executing one iteration. The mutate function on
// Loop (AppendIteration) handles ring-buffer trimming, NextRunAt
// computation, and max-iterations completion.
//
// Refuses to record if the loop has been paused/stopped/completed since
// the iteration started — returns ErrLoopNotRunning. The caller should
// skip downstream side effects (markdown writes, notifications) in that
// case, since the user has explicitly walked away from this loop.
//
// Returns ErrLoopGone when the loop has been removed entirely. Callers
// distinguish: NotRunning means "respect the new status, don't update
// it"; Gone means "everything related to this loop should be skipped".
func (m *Manager) RecordIteration(id string, it Iteration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.loops[id]
	if !ok {
		return ErrLoopGone
	}
	if l.Status != StatusRunning {
		return fmt.Errorf("%w (current status: %s)", ErrLoopNotRunning, l.Status)
	}
	l.AppendIteration(it)
	return m.storage.Save(l)
}

// transition is the canonical mutate-and-save path. Holds the write
// lock for the full Read-Modify-Save cycle so no concurrent caller can
// see a half-mutated loop or save stale state. The mutate function
// receives the live pointer (NOT a clone) — it's called under the lock.
//
// Snapshot-and-rollback: a pre-mutation deep copy is kept on the stack.
// If Storage.Save fails, the in-memory loop is restored from the
// snapshot so callers don't observe a divergent state ("disk says
// status=running, memory says status=paused"). Without this rollback a
// pause/resume failure would leave the user with a wrong-status loop in
// memory until restart.
func (m *Manager) transition(id string, mutate func(*Loop) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.loops[id]
	if !ok {
		return fmt.Errorf("loop %s: not found", id)
	}
	snapshot := cloneLoop(l)
	if err := mutate(l); err != nil {
		return err
	}
	if err := m.storage.Save(l); err != nil {
		*l = *snapshot
		return err
	}
	return nil
}

// cloneLoop is a defensive deep copy used by Get/List so callers can
// hold the returned pointer indefinitely without racing with manager
// mutations. The shallow-copy of the Iterations slice is OK because
// Iteration values are immutable after creation.
func cloneLoop(l *Loop) *Loop {
	c := *l
	if len(l.Iterations) > 0 {
		c.Iterations = make([]Iteration, len(l.Iterations))
		copy(c.Iterations, l.Iterations)
	}
	return &c
}

// sortLoopsForDisplay puts oldest first (so the listing reads as
// chronological history) with ID as the tiebreaker (for determinism).
func sortLoopsForDisplay(loops []*Loop) {
	// Use insertion sort — len(loops) is typically small (<10).
	for i := 1; i < len(loops); i++ {
		j := i
		for j > 0 && loopLess(loops[j], loops[j-1]) {
			loops[j], loops[j-1] = loops[j-1], loops[j]
			j--
		}
	}
}

func loopLess(a, b *Loop) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.ID < b.ID
}

func previewTask(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
