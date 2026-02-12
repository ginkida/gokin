package app

import (
	"sync"
	"time"
)

// ReliabilityManager tracks runtime health and enables safe degraded mode
// when repeated transient failures are detected.
type ReliabilityManager struct {
	mu sync.RWMutex

	consecutiveFailures int
	windowFailures      int
	windowStart         time.Time
	degradedUntil       time.Time
}

const (
	reliabilityFailureWindow = 2 * time.Minute
	reliabilityDegradeTime   = 5 * time.Minute
	reliabilityFailureLimit  = 3
)

func NewReliabilityManager() *ReliabilityManager {
	return &ReliabilityManager{
		windowStart: time.Now(),
	}
}

// RecordFailure updates failure counters and enables degraded mode when error
// budgets are exceeded.
func (r *ReliabilityManager) RecordFailure() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	if now.Sub(r.windowStart) > reliabilityFailureWindow {
		r.windowStart = now
		r.windowFailures = 0
	}

	r.consecutiveFailures++
	r.windowFailures++

	if r.consecutiveFailures >= reliabilityFailureLimit || r.windowFailures >= reliabilityFailureLimit {
		r.degradedUntil = now.Add(reliabilityDegradeTime)
	}
}

// RecordSuccess decays failure state after a successful request.
func (r *ReliabilityManager) RecordSuccess() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consecutiveFailures = 0
}

// IsDegraded returns true when safe degraded mode is active.
func (r *ReliabilityManager) IsDegraded() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return time.Now().Before(r.degradedUntil)
}

// DegradedRemaining returns how long degraded mode will remain active.
func (r *ReliabilityManager) DegradedRemaining() time.Duration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if time.Now().After(r.degradedUntil) {
		return 0
	}
	return time.Until(r.degradedUntil).Round(time.Second)
}

type ReliabilitySnapshot struct {
	ConsecutiveFailures int
	WindowFailures      int
	WindowStartedAt     time.Time
	Degraded            bool
	DegradedRemaining   time.Duration
}

func (r *ReliabilityManager) Snapshot() ReliabilitySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	now := time.Now()
	remaining := time.Duration(0)
	degraded := now.Before(r.degradedUntil)
	if degraded {
		remaining = time.Until(r.degradedUntil).Round(time.Second)
	}

	return ReliabilitySnapshot{
		ConsecutiveFailures: r.consecutiveFailures,
		WindowFailures:      r.windowFailures,
		WindowStartedAt:     r.windowStart,
		Degraded:            degraded,
		DegradedRemaining:   remaining,
	}
}
