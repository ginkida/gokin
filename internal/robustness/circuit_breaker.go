package robustness

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrCircuitOpen = errors.New("circuit breaker is open")
)

type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half_open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

type CircuitBreaker struct {
	mu           sync.RWMutex
	state        State
	failures     int
	threshold    int
	resetTimeout time.Duration
	lastFailure  time.Time

	// Optional rate window: when non-zero, recordFailure trips the breaker once
	// len(failureTimes) within the window reaches threshold. Complements the
	// classic cumulative-failure mode — useful for "N overload errors in M
	// minutes" patterns where consecutive-count is too sensitive.
	window       time.Duration
	failureTimes []time.Time
}

func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		state:        StateClosed,
	}
}

// NewWindowedCircuitBreaker creates a breaker that trips when `threshold`
// failures are recorded within the rolling `window` duration, independent of
// how many successes happened in between. After tripping the breaker behaves
// identically to the classic one (StateOpen → HalfOpen after resetTimeout).
//
// Use this for rate-limit / overload detection where a single success between
// failures shouldn't clear a genuine service-level problem — e.g. GLM 1305
// errors that come and go but indicate capacity issues.
func NewWindowedCircuitBreaker(threshold int, window, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
		state:        StateClosed,
		window:       window,
		failureTimes: make([]time.Time, 0, threshold+1),
	}
}

func (cb *CircuitBreaker) Execute(ctx context.Context, fn func() error) error {
	if !cb.allowRequest() {
		return ErrCircuitOpen
	}

	err := fn()
	if err != nil {
		cb.recordFailure()
		return err
	}

	cb.recordSuccess()
	return nil
}

func (cb *CircuitBreaker) allowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateClosed {
		return true
	}

	if cb.state == StateOpen {
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			// Allow a probe request in half-open mode.
			cb.state = StateHalfOpen
			return true
		}
		return false
	}

	return true // Half-open
}

// CanExecute returns true if the circuit would allow request execution now.
// This is a read-only query that does not transition state.
func (cb *CircuitBreaker) CanExecute() bool {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if cb.state == StateClosed {
		return true
	}
	if cb.state == StateOpen {
		return time.Since(cb.lastFailure) > cb.resetTimeout
	}
	return true // Half-open
}

func (cb *CircuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	cb.failures++
	cb.lastFailure = now

	// Windowed mode: drop failure timestamps older than the window before
	// counting toward the threshold. Classic mode ignores the ring buffer.
	if cb.window > 0 {
		cutoff := now.Add(-cb.window)
		kept := cb.failureTimes[:0]
		for _, t := range cb.failureTimes {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		cb.failureTimes = append(kept, now)
		if cb.state == StateHalfOpen || len(cb.failureTimes) >= cb.threshold {
			cb.state = StateOpen
		}
		return
	}

	if cb.state == StateHalfOpen || cb.failures >= cb.threshold {
		cb.state = StateOpen
	}
}

func (cb *CircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateHalfOpen {
		cb.state = StateClosed
		cb.failures = 0
		cb.failureTimes = cb.failureTimes[:0]
	} else if cb.state == StateClosed {
		cb.failures = 0
		// Windowed mode: a single success doesn't clear the window — we rely on
		// time-based expiry inside recordFailure. Leaving failureTimes intact
		// prevents a burst "1 success between 3 failures" from silently resetting.
	}
}

func (cb *CircuitBreaker) GetState() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Reset forces the circuit breaker back to closed state with zero failures.
// Used after provider failover to give the new provider a clean slate.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failures = 0
	if cb.failureTimes != nil {
		cb.failureTimes = cb.failureTimes[:0]
	}
}

// WindowedFailureCount returns the number of failures currently inside the
// rolling window (0 for breakers created without NewWindowedCircuitBreaker).
// Used by rate-based failover triggers to check pressure without allocating
// a probe request.
func (cb *CircuitBreaker) WindowedFailureCount() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	if cb.window == 0 {
		return 0
	}
	cutoff := time.Now().Add(-cb.window)
	count := 0
	for _, t := range cb.failureTimes {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}
