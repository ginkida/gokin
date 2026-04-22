package app

import (
	"context"
	"errors"
	"testing"
)

func TestPolicyEngine_New_CreatesAllBreakers(t *testing.T) {
	p := NewPolicyEngine()
	if p.requestBreaker == nil {
		t.Error("requestBreaker is nil")
	}
	if p.stepBreaker == nil {
		t.Error("stepBreaker is nil")
	}
	if p.overloadBreaker == nil {
		t.Error("overloadBreaker is nil")
	}
}

func TestPolicyEngine_RecordOverload_TripsAtThreshold(t *testing.T) {
	p := NewPolicyEngine()
	if p.RecordOverload() {
		t.Error("1st RecordOverload should not trip")
	}
	if p.RecordOverload() {
		t.Error("2nd RecordOverload should not trip")
	}
	if !p.RecordOverload() {
		t.Errorf("3rd RecordOverload should trip (threshold=%d)", overloadThreshold)
	}
}

func TestPolicyEngine_OverloadPressure_TracksCount(t *testing.T) {
	p := NewPolicyEngine()
	if got := p.OverloadPressure(); got != 0 {
		t.Errorf("fresh pressure = %d, want 0", got)
	}
	p.RecordOverload()
	p.RecordOverload()
	if got := p.OverloadPressure(); got != 2 {
		t.Errorf("pressure after 2 records = %d, want 2", got)
	}
}

func TestPolicyEngine_ExecuteRequest_PassesSuccess(t *testing.T) {
	p := NewPolicyEngine()
	called := false
	err := p.ExecuteRequest(context.Background(), func() error {
		called = true
		return nil
	})
	if err != nil {
		t.Errorf("success path err = %v", err)
	}
	if !called {
		t.Error("fn was not invoked")
	}
}

func TestPolicyEngine_ExecuteRequest_PropagatesError(t *testing.T) {
	p := NewPolicyEngine()
	want := errors.New("downstream boom")
	err := p.ExecuteRequest(context.Background(), func() error { return want })
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want wrap of %v", err, want)
	}
}

func TestPolicyEngine_ExecuteRequest_OpensAfterRepeatedFailures(t *testing.T) {
	p := NewPolicyEngine()
	// Request breaker threshold is 5 (see NewPolicyEngine).
	for range 5 {
		_ = p.ExecuteRequest(context.Background(), func() error { return errors.New("fail") })
	}

	called := false
	err := p.ExecuteRequest(context.Background(), func() error {
		called = true
		return nil
	})
	if called {
		t.Error("fn should not run while breaker is open")
	}
	// PolicyEngine intentionally transforms robustness.ErrCircuitOpen into the
	// app-level ErrRequestCircuitOpen so callers speak only in app sentinels.
	if !errors.Is(err, ErrRequestCircuitOpen) {
		t.Errorf("err = %v, want wrap of ErrRequestCircuitOpen", err)
	}
}

func TestPolicyEngine_ExecutePlanStep_IndependentOfRequestBreaker(t *testing.T) {
	p := NewPolicyEngine()
	// Trip only the request breaker.
	for range 5 {
		_ = p.ExecuteRequest(context.Background(), func() error { return errors.New("fail") })
	}

	ran := false
	err := p.ExecutePlanStep(context.Background(), func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Errorf("plan step should succeed while request breaker is open: %v", err)
	}
	if !ran {
		t.Error("plan-step fn should have run")
	}
}

func TestPolicyEngine_ExecutePlanStep_OpensAfterItsOwnFailures(t *testing.T) {
	p := NewPolicyEngine()
	// Step breaker threshold is 6 (see NewPolicyEngine).
	for range 6 {
		_ = p.ExecutePlanStep(context.Background(), func() error { return errors.New("fail") })
	}
	err := p.ExecutePlanStep(context.Background(), func() error { return nil })
	if !errors.Is(err, ErrStepCircuitOpen) {
		t.Errorf("err = %v, want wrap of ErrStepCircuitOpen", err)
	}
}

func TestPolicyEngine_ResetBreakers_RestoresAllToClosed(t *testing.T) {
	p := NewPolicyEngine()
	// Trip both breakers and the overload window.
	for range 5 {
		_ = p.ExecuteRequest(context.Background(), func() error { return errors.New("fail") })
	}
	for range 6 {
		_ = p.ExecutePlanStep(context.Background(), func() error { return errors.New("fail") })
	}
	p.RecordOverload()
	p.RecordOverload()
	p.RecordOverload()

	p.ResetBreakers()

	if got := p.OverloadPressure(); got != 0 {
		t.Errorf("post-reset overload pressure = %d, want 0", got)
	}
	ran := false
	err := p.ExecuteRequest(context.Background(), func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Errorf("post-reset request err = %v", err)
	}
	if !ran {
		t.Error("post-reset request fn should run")
	}

	ran = false
	err = p.ExecutePlanStep(context.Background(), func() error {
		ran = true
		return nil
	})
	if err != nil {
		t.Errorf("post-reset step err = %v", err)
	}
	if !ran {
		t.Error("post-reset step fn should run")
	}
}

func TestPolicyEngine_Snapshot_ReportsBreakerStates(t *testing.T) {
	p := NewPolicyEngine()
	snap := p.Snapshot()
	if snap.RequestBreakerState == "" {
		t.Error("RequestBreakerState empty")
	}
	if snap.StepBreakerState == "" {
		t.Error("StepBreakerState empty")
	}
}

func TestPolicyEngine_NilReceiverSafe(t *testing.T) {
	var p *PolicyEngine

	if p.RecordOverload() {
		t.Error("nil RecordOverload should return false")
	}
	if got := p.OverloadPressure(); got != 0 {
		t.Errorf("nil OverloadPressure = %d, want 0", got)
	}
	if err := p.ExecuteRequest(context.Background(), func() error { return nil }); err != nil {
		t.Errorf("nil ExecuteRequest err = %v", err)
	}
	if err := p.ExecutePlanStep(context.Background(), func() error { return nil }); err != nil {
		t.Errorf("nil ExecutePlanStep err = %v", err)
	}
	p.ResetBreakers()
	snap := p.Snapshot()
	if snap.RequestBreakerState != "" || snap.StepBreakerState != "" {
		t.Errorf("nil Snapshot should be zero-value, got %+v", snap)
	}
}

func TestPolicyEngine_ExecuteRequest_NilFnBreakerStillRuns(t *testing.T) {
	// Sanity: nil fn would panic inside robustness.Execute, so engine should
	// never be called with one. But verify the zero-state path (no breaker
	// yet because engine is nil-safe) does not panic.
	var p *PolicyEngine
	err := p.ExecuteRequest(context.Background(), func() error { return nil })
	if err != nil {
		t.Errorf("nil engine should pass through: %v", err)
	}
}
