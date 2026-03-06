package plan

import (
	"testing"
)

func TestLifecycleIsTerminal(t *testing.T) {
	tests := []struct {
		state Lifecycle
		want  bool
	}{
		{LifecycleDraft, false},
		{LifecycleAwaitingApproval, false},
		{LifecycleApproved, false},
		{LifecycleExecuting, false},
		{LifecyclePaused, false},
		{LifecycleFailed, false},
		{LifecycleCompleted, true},
		{LifecycleCancelled, true},
	}
	for _, tt := range tests {
		if got := tt.state.IsTerminal(); got != tt.want {
			t.Errorf("%q.IsTerminal() = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestLifecycleCanTransitionTo(t *testing.T) {
	tests := []struct {
		from Lifecycle
		to   Lifecycle
		want bool
	}{
		// Valid transitions
		{LifecycleDraft, LifecycleAwaitingApproval, true},
		{LifecycleDraft, LifecycleCancelled, true},
		{LifecycleAwaitingApproval, LifecycleApproved, true},
		{LifecycleAwaitingApproval, LifecycleCancelled, true},
		{LifecycleApproved, LifecycleExecuting, true},
		{LifecycleApproved, LifecycleCancelled, true},
		{LifecycleExecuting, LifecyclePaused, true},
		{LifecycleExecuting, LifecycleCompleted, true},
		{LifecycleExecuting, LifecycleFailed, true},
		{LifecycleExecuting, LifecycleCancelled, true},
		{LifecyclePaused, LifecycleApproved, true},
		{LifecyclePaused, LifecycleExecuting, true},
		{LifecyclePaused, LifecycleCancelled, true},
		{LifecycleFailed, LifecycleApproved, true},
		{LifecycleFailed, LifecycleExecuting, true},
		{LifecycleFailed, LifecycleCancelled, true},

		// Self-transitions (always valid)
		{LifecycleDraft, LifecycleDraft, true},
		{LifecycleExecuting, LifecycleExecuting, true},
		{LifecycleCompleted, LifecycleCompleted, true},

		// Invalid transitions
		{LifecycleDraft, LifecycleExecuting, false},
		{LifecycleDraft, LifecycleCompleted, false},
		{LifecycleCompleted, LifecycleDraft, false},
		{LifecycleCompleted, LifecycleExecuting, false},
		{LifecycleCancelled, LifecycleDraft, false},
		{LifecycleCancelled, LifecycleExecuting, false},
		{LifecycleAwaitingApproval, LifecycleExecuting, false},
		{LifecycleApproved, LifecyclePaused, false},
	}
	for _, tt := range tests {
		got := tt.from.CanTransitionTo(tt.to)
		if got != tt.want {
			t.Errorf("%q.CanTransitionTo(%q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestPlanTransitionLifecycle(t *testing.T) {
	p := NewPlan("Test", "")

	// Draft -> AwaitingApproval
	if err := p.TransitionLifecycle(LifecycleAwaitingApproval); err != nil {
		t.Errorf("draft -> awaiting: %v", err)
	}
	if p.LifecycleState() != LifecycleAwaitingApproval {
		t.Errorf("state = %v", p.LifecycleState())
	}

	// AwaitingApproval -> Approved
	if err := p.TransitionLifecycle(LifecycleApproved); err != nil {
		t.Errorf("awaiting -> approved: %v", err)
	}

	// Approved -> Executing
	if err := p.TransitionLifecycle(LifecycleExecuting); err != nil {
		t.Errorf("approved -> executing: %v", err)
	}

	// Executing -> Completed
	if err := p.TransitionLifecycle(LifecycleCompleted); err != nil {
		t.Errorf("executing -> completed: %v", err)
	}

	// Completed is terminal — cannot transition to executing
	if err := p.TransitionLifecycle(LifecycleExecuting); err == nil {
		t.Error("completed -> executing should fail")
	}
}

func TestPlanTransitionLifecycleInfersFromStatus(t *testing.T) {
	p := NewPlan("Test", "")
	p.mu.Lock()
	p.Lifecycle = "" // Simulate old plan without lifecycle
	p.Status = StatusInProgress
	p.mu.Unlock()

	// Should infer "executing" from StatusInProgress
	state := p.LifecycleState()
	if state != LifecycleExecuting {
		t.Errorf("inferred state = %v, want executing", state)
	}

	// Can transition from inferred state
	if err := p.TransitionLifecycle(LifecyclePaused); err != nil {
		t.Errorf("executing -> paused: %v", err)
	}
}

func TestInferLifecycleFromStatus(t *testing.T) {
	tests := []struct {
		status Status
		want   Lifecycle
	}{
		{StatusInProgress, LifecycleExecuting},
		{StatusPaused, LifecyclePaused},
		{StatusCompleted, LifecycleCompleted},
		{StatusFailed, LifecycleFailed},
		{StatusPending, LifecycleDraft},
		{StatusSkipped, LifecycleDraft},
	}
	for _, tt := range tests {
		got := inferLifecycleFromStatus(tt.status)
		if got != tt.want {
			t.Errorf("inferLifecycleFromStatus(%v) = %v, want %v", tt.status, got, tt.want)
		}
	}
}
