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
