package app

import (
	"sync"
	"testing"
	"time"
)

func TestHealthToastLimiter_FirstEmitPasses(t *testing.T) {
	l := newHealthToastLimiter()
	if !l.ShouldEmit("srv", time.Unix(100, 0)) {
		t.Error("first emit should pass")
	}
}

func TestHealthToastLimiter_WithinCooldownSuppressed(t *testing.T) {
	l := newHealthToastLimiter()
	base := time.Unix(100, 0)
	_ = l.ShouldEmit("srv", base)
	// Within cooldown — should be suppressed.
	if l.ShouldEmit("srv", base.Add(5*time.Second)) {
		t.Error("emit within cooldown should be suppressed")
	}
	if l.ShouldEmit("srv", base.Add(29*time.Second)) {
		t.Error("emit at 29s (< 30s cooldown) should be suppressed")
	}
}

func TestHealthToastLimiter_AfterCooldownEmitsAgain(t *testing.T) {
	l := newHealthToastLimiter()
	base := time.Unix(100, 0)
	_ = l.ShouldEmit("srv", base)
	// Clearly past cooldown — should emit.
	if !l.ShouldEmit("srv", base.Add(45*time.Second)) {
		t.Error("emit past cooldown should pass")
	}
	// That emit just reset the clock. Now we're "clearly within" again.
	if l.ShouldEmit("srv", base.Add(46*time.Second)) {
		t.Error("46s is within new cooldown from the 45s emit")
	}
}

func TestHealthToastLimiter_BoundarySemantics(t *testing.T) {
	// Pin the strict-< semantics: at exactly cooldown duration, the emit
	// passes. Important because changing to <= would suppress the first
	// flip after a full cooldown period, which would be user-confusing.
	l := newHealthToastLimiter()
	base := time.Unix(100, 0)
	_ = l.ShouldEmit("srv", base)
	if !l.ShouldEmit("srv", base.Add(healthToastCooldown)) {
		t.Errorf("emit at exact cooldown (strict <) should pass")
	}
}

func TestHealthToastLimiter_PerServerIndependent(t *testing.T) {
	l := newHealthToastLimiter()
	base := time.Unix(100, 0)
	if !l.ShouldEmit("srv-a", base) {
		t.Fatal("srv-a first emit")
	}
	// srv-b should emit even though srv-a is still in cooldown.
	if !l.ShouldEmit("srv-b", base.Add(1*time.Second)) {
		t.Error("srv-b should not be blocked by srv-a cooldown")
	}
	// srv-a still suppressed.
	if l.ShouldEmit("srv-a", base.Add(2*time.Second)) {
		t.Error("srv-a still within own cooldown")
	}
}

func TestHealthToastLimiter_ConcurrentSafe(t *testing.T) {
	l := newHealthToastLimiter()
	now := time.Unix(100, 0)
	var wg sync.WaitGroup
	emitCount := 0
	var mu sync.Mutex
	const callers = 100
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// All at the same timestamp — exactly one should win.
			if l.ShouldEmit("srv", now) {
				mu.Lock()
				emitCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if emitCount != 1 {
		t.Errorf("concurrent emit count = %d, want exactly 1 (race lost?)", emitCount)
	}
}
