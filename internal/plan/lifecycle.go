package plan

import (
	"fmt"
	"time"
)

// Lifecycle represents the high-level lifecycle state of a plan.
type Lifecycle string

const (
	LifecycleDraft            Lifecycle = "draft"
	LifecycleAwaitingApproval Lifecycle = "awaiting_approval"
	LifecycleApproved         Lifecycle = "approved"
	LifecycleExecuting        Lifecycle = "executing"
	LifecyclePaused           Lifecycle = "paused"
	LifecycleCompleted        Lifecycle = "completed"
	LifecycleFailed           Lifecycle = "failed"
	LifecycleCancelled        Lifecycle = "cancelled"
)

var allowedLifecycleTransitions = map[Lifecycle]map[Lifecycle]bool{
	LifecycleDraft: {
		LifecycleAwaitingApproval: true,
		LifecycleCancelled:        true,
	},
	LifecycleAwaitingApproval: {
		LifecycleApproved:  true,
		LifecycleCancelled: true,
	},
	LifecycleApproved: {
		LifecycleExecuting: true,
		LifecycleCancelled: true,
	},
	LifecycleExecuting: {
		LifecyclePaused:    true,
		LifecycleCompleted: true,
		LifecycleFailed:    true,
		LifecycleCancelled: true,
	},
	LifecyclePaused: {
		LifecycleApproved:  true,
		LifecycleExecuting: true,
		LifecycleCancelled: true,
	},
	LifecycleFailed: {
		LifecycleApproved:  true,
		LifecycleExecuting: true,
		LifecycleCancelled: true,
	},
	LifecycleCompleted: {},
	LifecycleCancelled: {},
}

// IsTerminal returns true if the lifecycle state is terminal.
func (l Lifecycle) IsTerminal() bool {
	return l == LifecycleCompleted || l == LifecycleCancelled
}

// CanTransitionTo returns true if transition from current state to next is valid.
func (l Lifecycle) CanTransitionTo(next Lifecycle) bool {
	if l == next {
		return true
	}
	allowed, ok := allowedLifecycleTransitions[l]
	if !ok {
		return false
	}
	return allowed[next]
}

// LifecycleState returns the current lifecycle state of the plan.
func (p *Plan) LifecycleState() Lifecycle {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.Lifecycle == "" {
		return inferLifecycleFromStatus(p.Status)
	}
	return p.Lifecycle
}

// TransitionLifecycle validates and applies a lifecycle transition.
func (p *Plan) TransitionLifecycle(next Lifecycle) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	current := p.Lifecycle
	if current == "" {
		current = inferLifecycleFromStatus(p.Status)
		p.Lifecycle = current
	}

	if !current.CanTransitionTo(next) {
		return fmt.Errorf("invalid plan lifecycle transition: %s -> %s", current, next)
	}

	p.Lifecycle = next
	p.UpdatedAt = time.Now()
	return nil
}

func inferLifecycleFromStatus(status Status) Lifecycle {
	switch status {
	case StatusInProgress:
		return LifecycleExecuting
	case StatusPaused:
		return LifecyclePaused
	case StatusCompleted:
		return LifecycleCompleted
	case StatusFailed:
		return LifecycleFailed
	default:
		return LifecycleDraft
	}
}
