package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gokin/internal/robustness"
)

var (
	ErrRequestCircuitOpen = errors.New("request circuit open")
	ErrStepCircuitOpen    = errors.New("plan-step circuit open")
)

// PolicyEngine centralizes runtime execution policies for request and plan-step
// execution paths (circuit breakers, degraded-mode gating).
type PolicyEngine struct {
	requestBreaker *robustness.CircuitBreaker
	stepBreaker    *robustness.CircuitBreaker
}

func NewPolicyEngine() *PolicyEngine {
	return &PolicyEngine{
		requestBreaker: robustness.NewCircuitBreaker(5, 45*time.Second),
		stepBreaker:    robustness.NewCircuitBreaker(6, 30*time.Second),
	}
}

// ExecuteRequest runs fn under request-level circuit breaker policy.
func (p *PolicyEngine) ExecuteRequest(ctx context.Context, fn func() error) error {
	if p == nil || p.requestBreaker == nil {
		return fn()
	}

	err := p.requestBreaker.Execute(ctx, fn)
	if errors.Is(err, robustness.ErrCircuitOpen) {
		return fmt.Errorf("%w: too many recent request failures", ErrRequestCircuitOpen)
	}
	return err
}

// ExecutePlanStep runs fn under plan-step circuit breaker policy.
func (p *PolicyEngine) ExecutePlanStep(ctx context.Context, fn func() error) error {
	if p == nil || p.stepBreaker == nil {
		return fn()
	}

	err := p.stepBreaker.Execute(ctx, fn)
	if errors.Is(err, robustness.ErrCircuitOpen) {
		return fmt.Errorf("%w: too many recent step failures", ErrStepCircuitOpen)
	}
	return err
}

type PolicySnapshot struct {
	RequestBreakerState string
	StepBreakerState    string
}

func (p *PolicyEngine) Snapshot() PolicySnapshot {
	if p == nil {
		return PolicySnapshot{}
	}
	s := PolicySnapshot{}
	if p.requestBreaker != nil {
		s.RequestBreakerState = p.requestBreaker.GetState().String()
	}
	if p.stepBreaker != nil {
		s.StepBreakerState = p.stepBreaker.GetState().String()
	}
	return s
}
