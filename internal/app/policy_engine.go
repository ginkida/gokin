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
	requestBreaker  *robustness.CircuitBreaker
	stepBreaker     *robustness.CircuitBreaker
	overloadBreaker *robustness.CircuitBreaker // windowed: tracks overload-style errors
}

// Thresholds for the windowed overload breaker. Tuned for GLM 1305 patterns:
// three overload signals inside five minutes means the provider is under real
// pressure and we should skip the usual 2-retry-on-same-provider wait.
const (
	overloadWindow    = 5 * time.Minute
	overloadThreshold = 3
	overloadReset     = 5 * time.Minute
)

func NewPolicyEngine() *PolicyEngine {
	return &PolicyEngine{
		requestBreaker:  robustness.NewCircuitBreaker(5, 45*time.Second),
		stepBreaker:     robustness.NewCircuitBreaker(6, 30*time.Second),
		overloadBreaker: robustness.NewWindowedCircuitBreaker(overloadThreshold, overloadWindow, overloadReset),
	}
}

// RecordOverload should be called once per detected provider-side overload
// (GLM 1305, z.ai "overloaded" strings, HTTP 529 etc.). Returns true iff the
// windowed breaker is now open — the caller should treat this as a signal to
// failover immediately rather than keep retrying on the same provider.
func (p *PolicyEngine) RecordOverload() bool {
	if p == nil || p.overloadBreaker == nil {
		return false
	}
	// Execute with a no-op failing fn just to record the failure without
	// actually running anything under the breaker.
	_ = p.overloadBreaker.Execute(context.Background(), func() error { return errOverload })
	return !p.overloadBreaker.CanExecute()
}

// OverloadPressure returns the number of overload events inside the current
// rolling window. Used for telemetry / UI hints.
func (p *PolicyEngine) OverloadPressure() int {
	if p == nil || p.overloadBreaker == nil {
		return 0
	}
	return p.overloadBreaker.WindowedFailureCount()
}

var errOverload = errors.New("provider overloaded")

// ExecuteRequest runs fn under request-level circuit breaker policy.
func (p *PolicyEngine) ExecuteRequest(ctx context.Context, fn func() error) error {
	if p == nil || p.requestBreaker == nil {
		return fn()
	}

	err := p.requestBreaker.Execute(ctx, fn)
	if errors.Is(err, robustness.ErrCircuitOpen) {
		return fmt.Errorf("%w: API temporarily unavailable (rate limited or down) — try again in a minute or switch provider with /model", ErrRequestCircuitOpen)
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

// ResetBreakers resets both circuit breakers to closed state.
// Called after provider failover to give the new provider a clean slate.
func (p *PolicyEngine) ResetBreakers() {
	if p == nil {
		return
	}
	if p.requestBreaker != nil {
		p.requestBreaker.Reset()
	}
	if p.stepBreaker != nil {
		p.stepBreaker.Reset()
	}
	if p.overloadBreaker != nil {
		p.overloadBreaker.Reset()
	}
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
