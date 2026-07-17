package app

import (
	"sync"
	"testing"
	"time"
)

func TestReliabilityManager_InitialState(t *testing.T) {
	r := NewReliabilityManager()
	if r == nil {
		t.Fatal("NewReliabilityManager returned nil")
	}
	if r.IsDegraded() {
		t.Error("fresh manager must not be degraded")
	}
	if rem := r.DegradedRemaining(); rem != 0 {
		t.Errorf("fresh manager DegradedRemaining = %v, want 0", rem)
	}

	snap := r.Snapshot()
	if snap.ConsecutiveFailures != 0 || snap.WindowFailures != 0 {
		t.Errorf("fresh snapshot counters = %d/%d, want 0/0",
			snap.ConsecutiveFailures, snap.WindowFailures)
	}
	if snap.Degraded {
		t.Error("fresh snapshot Degraded = true, want false")
	}
	if snap.DegradedRemaining != 0 {
		t.Errorf("fresh snapshot DegradedRemaining = %v, want 0", snap.DegradedRemaining)
	}
	if time.Since(snap.WindowStartedAt) > time.Minute {
		t.Errorf("WindowStartedAt = %v, want ~now", snap.WindowStartedAt)
	}
}

func TestReliabilityManager_ConsecutiveFailuresTriggerDegraded(t *testing.T) {
	r := NewReliabilityManager()

	for i := 1; i < reliabilityFailureLimit; i++ {
		r.RecordFailure()
		if r.IsDegraded() {
			t.Fatalf("degraded after %d failures, want threshold %d", i, reliabilityFailureLimit)
		}
	}

	r.RecordFailure()
	if !r.IsDegraded() {
		t.Fatal("not degraded after hitting the consecutive failure limit")
	}

	rem := r.DegradedRemaining()
	if rem <= 0 || rem > reliabilityDegradeTime {
		t.Errorf("DegradedRemaining = %v, want within (0, %v]", rem, reliabilityDegradeTime)
	}

	snap := r.Snapshot()
	if !snap.Degraded {
		t.Error("snapshot Degraded = false while manager is degraded")
	}
	if snap.ConsecutiveFailures != reliabilityFailureLimit {
		t.Errorf("snapshot ConsecutiveFailures = %d, want %d",
			snap.ConsecutiveFailures, reliabilityFailureLimit)
	}
	if snap.DegradedRemaining <= 0 {
		t.Error("snapshot DegradedRemaining must be positive while degraded")
	}
}

func TestReliabilityManager_WindowFailuresTriggerDegraded(t *testing.T) {
	r := NewReliabilityManager()

	// Two failures, then a success resets only the consecutive counter —
	// the window counter keeps accumulating.
	r.RecordFailure()
	r.RecordFailure()
	r.RecordSuccess()

	if r.IsDegraded() {
		t.Fatal("degraded before window limit reached")
	}

	// Third failure inside the window: consecutive is 1 (< limit) but the
	// window counter reaches the limit.
	r.RecordFailure()
	if !r.IsDegraded() {
		t.Fatal("not degraded after window failure limit reached")
	}

	snap := r.Snapshot()
	if snap.ConsecutiveFailures != 1 {
		t.Errorf("snapshot ConsecutiveFailures = %d, want 1 (reset by success)",
			snap.ConsecutiveFailures)
	}
	if snap.WindowFailures != reliabilityFailureLimit {
		t.Errorf("snapshot WindowFailures = %d, want %d",
			snap.WindowFailures, reliabilityFailureLimit)
	}
}

func TestReliabilityManager_WindowExpiryResetsWindowCounter(t *testing.T) {
	r := NewReliabilityManager()

	// Move the window start beyond the failure window so the next failure
	// starts a fresh window instead of accumulating.
	r.windowStart = time.Now().Add(-reliabilityFailureWindow - time.Second)

	r.RecordFailure()

	snap := r.Snapshot()
	if snap.WindowFailures != 1 {
		t.Errorf("WindowFailures = %d after window expiry, want 1 (fresh window)",
			snap.WindowFailures)
	}
	if time.Since(snap.WindowStartedAt) > time.Minute {
		t.Errorf("WindowStartedAt not refreshed after expiry: %v", snap.WindowStartedAt)
	}
	if r.IsDegraded() {
		t.Error("degraded with a single failure in a fresh window")
	}
}

func TestReliabilityManager_DegradedRemainingAfterExpiry(t *testing.T) {
	r := NewReliabilityManager()
	// Degraded period already in the past.
	r.degradedUntil = time.Now().Add(-time.Second)

	if r.IsDegraded() {
		t.Error("IsDegraded = true for an expired degradation")
	}
	if rem := r.DegradedRemaining(); rem != 0 {
		t.Errorf("DegradedRemaining = %v for an expired degradation, want 0", rem)
	}
	if snap := r.Snapshot(); snap.Degraded || snap.DegradedRemaining != 0 {
		t.Errorf("snapshot = %+v for an expired degradation, want Degraded=false, Remaining=0", snap)
	}
}

func TestReliabilityManager_ConcurrentAccess(t *testing.T) {
	r := NewReliabilityManager()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if (i+j)%3 == 0 {
					r.RecordSuccess()
				} else {
					r.RecordFailure()
				}
				_ = r.IsDegraded()
				_ = r.DegradedRemaining()
				_ = r.Snapshot()
			}
		}(i)
	}
	wg.Wait()
}
