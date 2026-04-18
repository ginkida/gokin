package robustness

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStateString(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateClosed, "closed"},
		{StateHalfOpen, "half_open"},
		{StateOpen, "open"},
		{State(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.state.String()
		if got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestNewCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(3, 5*time.Second)
	if cb.GetState() != StateClosed {
		t.Errorf("initial state = %v, want closed", cb.GetState())
	}
}

func TestCircuitBreakerSuccess(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)

	err := cb.Execute(context.Background(), func() error {
		return nil
	})
	if err != nil {
		t.Errorf("success should not return error: %v", err)
	}
	if cb.GetState() != StateClosed {
		t.Error("state should remain closed after success")
	}
}

func TestCircuitBreakerOpensOnFailures(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)
	testErr := errors.New("test error")

	// Fail 3 times (threshold)
	for i := 0; i < 3; i++ {
		err := cb.Execute(context.Background(), func() error {
			return testErr
		})
		if err != testErr {
			t.Errorf("should propagate error, got: %v", err)
		}
	}

	if cb.GetState() != StateOpen {
		t.Errorf("state = %v, want open after %d failures", cb.GetState(), 3)
	}
}

func TestCircuitBreakerRejectsWhenOpen(t *testing.T) {
	cb := NewCircuitBreaker(1, time.Hour) // Long reset timeout

	// Trip the breaker
	cb.Execute(context.Background(), func() error {
		return errors.New("fail")
	})

	if cb.GetState() != StateOpen {
		t.Fatal("should be open")
	}

	// Next call should be rejected
	err := cb.Execute(context.Background(), func() error {
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("should return ErrCircuitOpen, got: %v", err)
	}
}

func TestCircuitBreakerCanExecute(t *testing.T) {
	cb := NewCircuitBreaker(1, time.Hour)

	if !cb.CanExecute() {
		t.Error("should be executable when closed")
	}

	cb.Execute(context.Background(), func() error {
		return errors.New("fail")
	})

	if cb.CanExecute() {
		t.Error("should not be executable when open (long timeout)")
	}
}

func TestCircuitBreakerHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	// Trip the breaker
	cb.Execute(context.Background(), func() error {
		return errors.New("fail")
	})

	if cb.GetState() != StateOpen {
		t.Fatal("should be open")
	}

	// Wait for reset timeout
	time.Sleep(20 * time.Millisecond)

	// Should transition to half-open on next request
	err := cb.Execute(context.Background(), func() error {
		return nil // Success
	})
	if err != nil {
		t.Errorf("half-open success should work: %v", err)
	}
	if cb.GetState() != StateClosed {
		t.Errorf("should be closed after half-open success, got %v", cb.GetState())
	}
}

func TestCircuitBreakerHalfOpenFailure(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	// Trip the breaker
	cb.Execute(context.Background(), func() error {
		return errors.New("fail")
	})

	time.Sleep(20 * time.Millisecond)

	// Fail during half-open -> back to open
	cb.Execute(context.Background(), func() error {
		return errors.New("still failing")
	})

	if cb.GetState() != StateOpen {
		t.Errorf("should be open after half-open failure, got %v", cb.GetState())
	}
}

func TestCircuitBreakerResetOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)

	// 2 failures (below threshold)
	for i := 0; i < 2; i++ {
		cb.Execute(context.Background(), func() error {
			return errors.New("fail")
		})
	}

	// Success should reset counter
	cb.Execute(context.Background(), func() error {
		return nil
	})

	// 2 more failures should NOT open (counter was reset)
	for i := 0; i < 2; i++ {
		cb.Execute(context.Background(), func() error {
			return errors.New("fail")
		})
	}

	if cb.GetState() != StateClosed {
		t.Errorf("state = %v, want closed (counter should have reset)", cb.GetState())
	}
}

func TestWindowedCircuitBreaker_TripsOnRate(t *testing.T) {
	cb := NewWindowedCircuitBreaker(3, 500*time.Millisecond, time.Second)
	fail := func() error { return errors.New("boom") }

	// Two failures within the window — not enough to trip.
	_ = cb.Execute(context.Background(), fail)
	_ = cb.Execute(context.Background(), fail)
	if cb.GetState() != StateClosed {
		t.Fatalf("state after 2 failures = %v, want closed", cb.GetState())
	}

	// Third failure inside the window flips to open.
	_ = cb.Execute(context.Background(), fail)
	if cb.GetState() != StateOpen {
		t.Fatalf("state after 3 failures in window = %v, want open", cb.GetState())
	}
	if got := cb.WindowedFailureCount(); got != 3 {
		t.Errorf("WindowedFailureCount = %d, want 3", got)
	}
}

func TestWindowedCircuitBreaker_AgedFailuresDropped(t *testing.T) {
	cb := NewWindowedCircuitBreaker(3, 80*time.Millisecond, time.Second)
	fail := func() error { return errors.New("boom") }

	// Two failures, then wait past the window.
	_ = cb.Execute(context.Background(), fail)
	_ = cb.Execute(context.Background(), fail)
	time.Sleep(100 * time.Millisecond)

	// Next failure sees the window cleared — still closed.
	_ = cb.Execute(context.Background(), fail)
	if cb.GetState() != StateClosed {
		t.Errorf("state after old failures aged out = %v, want closed", cb.GetState())
	}
	if got := cb.WindowedFailureCount(); got != 1 {
		t.Errorf("WindowedFailureCount after aging = %d, want 1", got)
	}
}

func TestWindowedCircuitBreaker_SuccessDoesNotClearWindow(t *testing.T) {
	cb := NewWindowedCircuitBreaker(3, 500*time.Millisecond, time.Second)
	fail := func() error { return errors.New("boom") }
	ok := func() error { return nil }

	_ = cb.Execute(context.Background(), fail)
	_ = cb.Execute(context.Background(), ok) // success between failures
	_ = cb.Execute(context.Background(), fail)
	_ = cb.Execute(context.Background(), fail)

	// 3 failures accumulated in the window despite one success → should be open.
	if cb.GetState() != StateOpen {
		t.Errorf("state = %v, want open (success must not clear window)", cb.GetState())
	}
}

func TestWindowedCircuitBreaker_ResetClearsWindow(t *testing.T) {
	cb := NewWindowedCircuitBreaker(3, 500*time.Millisecond, time.Second)
	fail := func() error { return errors.New("boom") }

	for i := 0; i < 3; i++ {
		_ = cb.Execute(context.Background(), fail)
	}
	if cb.GetState() != StateOpen {
		t.Fatalf("precondition: state = %v, want open", cb.GetState())
	}

	cb.Reset()
	if cb.GetState() != StateClosed {
		t.Errorf("state after Reset = %v, want closed", cb.GetState())
	}
	if got := cb.WindowedFailureCount(); got != 0 {
		t.Errorf("WindowedFailureCount after Reset = %d, want 0", got)
	}
}

func TestClassicBreaker_WindowedCountIsZero(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)
	_ = cb.Execute(context.Background(), func() error { return errors.New("x") })
	if got := cb.WindowedFailureCount(); got != 0 {
		t.Errorf("classic breaker WindowedFailureCount = %d, want 0", got)
	}
}
