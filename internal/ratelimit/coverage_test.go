package ratelimit

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// approxEq checks two floats are equal within a small tolerance. Token counts
// drift by tiny fractions because Available() calls refill() on every read,
// adding elapsed*refillRate tokens for the microseconds since the last op.
func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 0.01
}

// ===========================================================================
// TokenBucket — UpdateParameters (0% → full)
// ===========================================================================

func TestUpdateParameters_ChangesCapacityAndRate(t *testing.T) {
	b := NewTokenBucket(10, 1) // 10 max, 1/sec
	b.TryConsume(5)            // 5 remaining

	// Increase capacity to 20, rate to 5/sec
	b.UpdateParameters(20, 5)

	if got := b.Capacity(); got != 20 {
		t.Errorf("Capacity = %v, want 20", got)
	}
	if got := b.refillRate; got != 5 {
		t.Errorf("refillRate = %v, want 5", got)
	}
	if got := b.Available(); !approxEq(got, 5) {
		t.Errorf("Available = %v, want ~5 (unchanged)", got)
	}
}

func TestUpdateParameters_CapsTokensAboveNewMax(t *testing.T) {
	b := NewTokenBucket(20, 1) // 20 max
	// Don't consume — full at 20

	// Shrink capacity below current tokens
	b.UpdateParameters(5, 1)

	if got := b.Available(); !approxEq(got, 5) {
		t.Errorf("Available = %v, want ~5 (capped to new max)", got)
	}
}

// ===========================================================================
// TokenBucket — EstimateWaitTime (0% → full)
// ===========================================================================

func TestEstimateWaitTime_ZeroWhenEnoughTokens(t *testing.T) {
	b := NewTokenBucket(10, 5)
	if w := b.EstimateWaitTime(5); w != 0 {
		t.Errorf("EstimateWaitTime(5) with 10 available = %v, want 0", w)
	}
}

func TestEstimateWaitTime_NonZeroWhenDeficit(t *testing.T) {
	b := NewTokenBucket(10, 5) // 5 tokens/sec
	b.TryConsume(8)            // 2 remaining

	// Need 7, have 2, deficit 5, rate 5/sec → 1s
	w := b.EstimateWaitTime(7)
	if w <= 0 {
		t.Errorf("EstimateWaitTime(7) with deficit = %v, want > 0", w)
	}
	// Should be roughly 1 second (5 tokens / 5 per sec)
	if w < 800*time.Millisecond || w > 1200*time.Millisecond {
		t.Errorf("EstimateWaitTime(7) = %v, want ~1s", w)
	}
}

func TestEstimateWaitTime_ClampsToMax(t *testing.T) {
	b := NewTokenBucket(5, 10)
	b.TryConsume(5) // empty

	// Request more than max — should clamp to max (5) for the deficit calc
	w := b.EstimateWaitTime(100)
	if w <= 0 {
		t.Errorf("EstimateWaitTime(100) clamped = %v, want > 0", w)
	}
}

func TestEstimateWaitTime_ZeroRateReturnsZero(t *testing.T) {
	b := NewTokenBucket(10, 0) // no refill
	b.TryConsume(10)           // empty

	// With refillRate=0, bucket will never refill → returns 0 (can't wait)
	if w := b.EstimateWaitTime(5); w != 0 {
		t.Errorf("EstimateWaitTime with 0 rate = %v, want 0", w)
	}
}

// ===========================================================================
// TokenBucket — Consume (blocking)
// ===========================================================================

func TestConsume_ImmediateWhenAvailable(t *testing.T) {
	b := NewTokenBucket(5, 1)
	done := make(chan struct{})
	go func() {
		b.Consume(3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Consume(3) with 5 available should return immediately")
	}
}

func TestConsume_ZeroRateDoesNotHang(t *testing.T) {
	b := NewTokenBucket(1, 0) // no refill
	b.TryConsume(1)           // empty

	done := make(chan struct{})
	go func() {
		// With 0 refill rate, Consume must not hang (allows immediately)
		b.Consume(1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Consume with 0 refill rate hung — should allow immediately")
	}
}

func TestConsume_BlocksUntilRefill(t *testing.T) {
	b := NewTokenBucket(1, 100) // 100 tokens/sec → 10ms per token
	b.TryConsume(1)             // empty

	done := make(chan struct{})
	start := time.Now()
	go func() {
		b.Consume(1)
		close(done)
	}()
	select {
	case <-done:
		elapsed := time.Since(start)
		// Should have waited ~10ms for refill
		if elapsed < 5*time.Millisecond {
			t.Errorf("Consume returned too fast: %v, expected to wait for refill", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Consume never returned after refill")
	}
}

// ===========================================================================
// TokenBucket — ConsumeWithTimeout
// ===========================================================================

func TestConsumeWithTimeout_SuccessWhenAvailable(t *testing.T) {
	b := NewTokenBucket(10, 1)
	if !b.ConsumeWithTimeout(5, 1*time.Second) {
		t.Error("ConsumeWithTimeout(5) with 10 available should succeed")
	}
}

func TestConsumeWithTimeout_TimesOut(t *testing.T) {
	b := NewTokenBucket(1, 0.01) // very slow refill: 0.01/sec = 100s per token
	b.TryConsume(1)              // empty

	// Need 1 token, deficit 1, rate 0.01/sec → 100s wait.
	// With 100ms timeout, should fail.
	if b.ConsumeWithTimeout(1, 100*time.Millisecond) {
		t.Error("ConsumeWithTimeout should fail (timeout) when deficit can't be filled in time")
	}
}

func TestConsumeWithTimeout_ZeroRateSucceedsImmediately(t *testing.T) {
	b := NewTokenBucket(1, 0)
	b.TryConsume(1) // empty

	// With 0 rate, ConsumeWithTimeout allows immediately (returns true)
	if !b.ConsumeWithTimeout(1, 100*time.Millisecond) {
		t.Error("ConsumeWithTimeout with 0 rate should succeed immediately")
	}
}

// ===========================================================================
// TokenBucket — ConsumeWithContext
// ===========================================================================

func TestConsumeWithContext_Success(t *testing.T) {
	b := NewTokenBucket(10, 1)
	if err := b.ConsumeWithContext(context.Background(), 5); err != nil {
		t.Errorf("ConsumeWithContext(5) with 10 available: %v", err)
	}
}

func TestConsumeWithContext_CancelledReturnsError(t *testing.T) {
	b := NewTokenBucket(1, 0.01) // slow refill
	b.TryConsume(1)              // empty

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the first wait select hits ctx.Done()
	cancel()

	err := b.ConsumeWithContext(ctx, 1)
	if err != context.Canceled {
		t.Errorf("ConsumeWithContext with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestConsumeWithContext_ZeroRateSucceeds(t *testing.T) {
	b := NewTokenBucket(1, 0)
	b.TryConsume(1) // empty

	if err := b.ConsumeWithContext(context.Background(), 1); err != nil {
		t.Errorf("ConsumeWithContext with 0 rate: %v", err)
	}
}

func TestConsumeWithContext_BlocksUntilRefill(t *testing.T) {
	b := NewTokenBucket(1, 100) // 10ms per token
	b.TryConsume(1)             // empty

	done := make(chan error, 1)
	go func() {
		done <- b.ConsumeWithContext(context.Background(), 1)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ConsumeWithContext after refill: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ConsumeWithContext never returned after refill")
	}
}

// ===========================================================================
// TokenBucket — Sync
// ===========================================================================

func TestSync_SetsTokenCount(t *testing.T) {
	b := NewTokenBucket(100, 1)
	b.TryConsume(50) // 50 remaining

	b.Sync(25)
	// Available() calls refill() which adds a tiny fractional amount for the
	// elapsed time since Sync — use a tolerance rather than exact equality.
	got := b.Available()
	if got < 24.9 || got > 25.5 {
		t.Errorf("Available after Sync(25) = %v, want ~25", got)
	}
}

func TestSync_ClampsToMax(t *testing.T) {
	b := NewTokenBucket(10, 1)
	b.Sync(1000) // way above max
	if got := b.Available(); !approxEq(got, 10) {
		t.Errorf("Available after Sync(1000) = %v, want ~10 (clamped)", got)
	}
}

// ===========================================================================
// TokenBucket — Return
// ===========================================================================

func TestReturn_AddsTokens(t *testing.T) {
	b := NewTokenBucket(10, 0)
	b.TryConsume(5) // 5 remaining
	b.Return(3)
	if got := b.Available(); !approxEq(got, 8) {
		t.Errorf("Available after Return(3) = %v, want ~8", got)
	}
}

func TestReturn_ClampsToMax(t *testing.T) {
	b := NewTokenBucket(10, 0)
	b.TryConsume(5) // 5 remaining
	b.Return(100)   // way above max
	if got := b.Available(); !approxEq(got, 10) {
		t.Errorf("Available after Return(100) = %v, want ~10 (clamped)", got)
	}
}

// ===========================================================================
// TokenBucket — refill cap (maxRefillSeconds)
// ===========================================================================

func TestRefill_CapsAtMaxRefillSeconds(t *testing.T) {
	b := NewTokenBucket(10, 5) // 5 tokens/sec
	// Simulate that last refill was far in the past (1 hour ago)
	b.mu.Lock()
	b.lastRefill = time.Now().Add(-1 * time.Hour)
	b.tokens = 0
	b.mu.Unlock()

	// Without the cap, refill would add 5 * 3600 = 18000 tokens.
	// With the 120s cap, it adds 5 * 120 = 600, then clamps to maxTokens=10.
	got := b.Available()
	if got != 10 {
		t.Errorf("Available after long sleep with cap = %v, want 10 (clamped to max)", got)
	}
}

// ===========================================================================
// Limiter — Acquire (22% → full)
// ===========================================================================

func TestLimiterAcquire_DisabledReturnsImmediately(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	if err := l.Acquire(100); err != nil {
		t.Errorf("Acquire on disabled limiter: %v", err)
	}
}

func TestLimiterAcquire_SuccessWithTokens(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	if err := l.Acquire(100); err != nil {
		t.Errorf("Acquire(100): %v", err)
	}
	// totalRequests should have incremented
	s := l.Stats()
	if s.TotalRequests != 1 {
		t.Errorf("TotalRequests = %d, want 1", s.TotalRequests)
	}
}

func TestLimiterAcquire_ZeroTokensSkipsTokenBucket(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         5,
	})
	// estimatedTokens=0 → only request bucket consumed
	if err := l.Acquire(0); err != nil {
		t.Errorf("Acquire(0): %v", err)
	}
}

// ===========================================================================
// Limiter — EstimateWaitTime (0% → full)
// ===========================================================================

func TestLimiterEstimateWaitTime_DisabledReturnsZero(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	if w := l.EstimateWaitTime(100); w != 0 {
		t.Errorf("EstimateWaitTime on disabled = %v, want 0", w)
	}
}

func TestLimiterEstimateWaitTime_ZeroWhenAvailable(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	if w := l.EstimateWaitTime(100); w != 0 {
		t.Errorf("EstimateWaitTime(100) with full buckets = %v, want 0", w)
	}
}

func TestLimiterEstimateWaitTime_NonZeroAfterExhaustion(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 6,  // 0.1/sec — slow refill
		TokensPerMinute:   60, // 1/sec
		BurstSize:         2,
	})
	// Exhaust request bucket
	l.TryAcquire(0)
	l.TryAcquire(0)
	// Now request bucket is empty
	w := l.EstimateWaitTime(0)
	if w <= 0 {
		t.Errorf("EstimateWaitTime after exhaustion = %v, want > 0", w)
	}
}

// ===========================================================================
// Limiter — TryAcquire (blocked path)
// ===========================================================================

func TestLimiterTryAcquire_BlockedByRequestLimit(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         2,
	})
	// Exhaust request bucket (burst=2)
	if !l.TryAcquire(0) {
		t.Fatal("first TryAcquire should succeed")
	}
	if !l.TryAcquire(0) {
		t.Fatal("second TryAcquire should succeed")
	}
	// Third should be blocked
	if l.TryAcquire(0) {
		t.Error("third TryAcquire should be blocked")
	}
	s := l.Stats()
	if s.BlockedRequests == 0 {
		t.Error("BlockedRequests should be > 0")
	}
}

func TestLimiterTryAcquire_BlockedByTokenLimit(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 10000,
		TokensPerMinute:   40, // burst = 4 tokens
		BurstSize:         10,
	})
	// Drain the token burst (4 tokens).
	if !l.TryAcquire(4) {
		t.Fatal("first TryAcquire(4) should succeed")
	}
	// Token bucket now empty, refill is slow (40/min = 0.67/sec).
	// Next TryAcquire should fail on the token bucket.
	if l.TryAcquire(1) {
		t.Error("TryAcquire(1) with empty token bucket should be blocked")
	}
	s := l.Stats()
	if s.BlockedRequests == 0 {
		t.Error("BlockedRequests should be > 0 after token-block")
	}
}

func TestLimiterTryAcquire_DisabledReturnsTrue(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	if !l.TryAcquire(100) {
		t.Error("TryAcquire on disabled should return true")
	}
}

// ===========================================================================
// Limiter — AcquireWithTimeout
// ===========================================================================

func TestLimiterAcquireWithTimeout_Disabled(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	if err := l.AcquireWithTimeout(100, 1*time.Second); err != nil {
		t.Errorf("AcquireWithTimeout on disabled: %v", err)
	}
}

func TestLimiterAcquireWithTimeout_Success(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	if err := l.AcquireWithTimeout(100, 1*time.Second); err != nil {
		t.Errorf("AcquireWithTimeout(100): %v", err)
	}
}

func TestLimiterAcquireWithTimeout_RequestLimitExceeded(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 6, // 0.1/sec refill
		TokensPerMinute:   10000,
		BurstSize:         1,
	})
	// Exhaust request bucket
	l.TryAcquire(0)
	// Next AcquireWithTimeout with short timeout should fail on request limit
	err := l.AcquireWithTimeout(0, 100*time.Millisecond)
	if err == nil {
		t.Error("AcquireWithTimeout should fail with request limit exceeded")
	}
}

func TestLimiterAcquireWithTimeout_TokenLimitExceeded(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 10000,
		TokensPerMinute:   6, // 0.1/sec refill, burst = 0.6 tokens
		BurstSize:         10,
	})
	// First call drains the tiny token burst (0.6 tokens, clamped).
	l.AcquireWithTimeout(1, 50*time.Millisecond)
	// Second call: token bucket is now empty, refill is 0.1/sec (10s/token),
	// so with a 50ms timeout the token acquisition must fail.
	err := l.AcquireWithTimeout(1, 50*time.Millisecond)
	if err == nil {
		t.Error("AcquireWithTimeout should fail with token limit exceeded")
	}
}

// ===========================================================================
// Limiter — AcquireWithContext (including danger-zone delay)
// ===========================================================================

func TestLimiterAcquireWithContext_Disabled(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	if err := l.AcquireWithContext(context.Background(), 100); err != nil {
		t.Errorf("AcquireWithContext on disabled: %v", err)
	}
}

func TestLimiterAcquireWithContext_Success(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   100000,
		BurstSize:         10,
	})
	if err := l.AcquireWithContext(context.Background(), 100); err != nil {
		t.Errorf("AcquireWithContext(100): %v", err)
	}
}

func TestLimiterAcquireWithContext_Cancelled(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 6, // slow refill
		TokensPerMinute:   10000,
		BurstSize:         1,
	})
	// Exhaust request bucket
	l.TryAcquire(0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := l.AcquireWithContext(ctx, 0)
	if err != context.Canceled {
		t.Errorf("AcquireWithContext with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestLimiterAcquireWithContext_DangerZoneDelay(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	// Drain to < 5% of capacity to trigger danger zone
	// request burst=10 → drain 10 (0% < 5%)
	for i := 0; i < 10; i++ {
		l.requestBucket.TryConsume(1)
	}
	// Refill is 1/sec, so after draining we're at ~0 → danger zone.
	// But we need SOME tokens for AcquireWithContext to succeed first.
	// Sync a tiny amount that's still < 5% of capacity.
	l.requestBucket.Sync(0.4) // 0.4 / 10 = 4% < 5% → danger

	start := time.Now()
	err := l.AcquireWithContext(context.Background(), 0)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("AcquireWithContext in danger zone: %v", err)
	}
	// Should have waited ~1s for the safety delay
	if elapsed < 900*time.Millisecond {
		t.Errorf("danger-zone delay too short: %v, want ~1s", elapsed)
	}
}

func TestLimiterAcquireWithContext_DangerZoneCancelled(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	// Drain request bucket to danger zone
	for i := 0; i < 10; i++ {
		l.requestBucket.TryConsume(1)
	}
	l.requestBucket.Sync(0.4) // 4% → danger

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := l.AcquireWithContext(ctx, 0)
	if err != context.DeadlineExceeded {
		t.Errorf("AcquireWithContext danger+timeout = %v, want context.DeadlineExceeded", err)
	}
}

// ===========================================================================
// Limiter — UpdateLimits
// ===========================================================================

func TestLimiterUpdateLimits_DisabledIsNoOp(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	// UpdateLimits on a disabled limiter must be a no-op: buckets stay nil-ish
	// and no panic occurs.
	l.UpdateLimits(100, 50, time.Minute, 1000, 500, time.Minute)

	// Stats on disabled limiter should reflect no activity.
	s := l.Stats()
	if s.TotalRequests != 0 {
		t.Errorf("TotalRequests = %d, want 0 (disabled)", s.TotalRequests)
	}
}

func TestLimiterUpdateLimits_UpdatesBuckets(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})

	// Simulate provider headers: 500 req limit, 400 remaining, 60s reset
	l.UpdateLimits(500, 400, 60*time.Second, 100000, 80000, 60*time.Second)

	// Request bucket should now have capacity 500, ~400 available
	if got := l.requestBucket.Capacity(); got != 500 {
		t.Errorf("request Capacity = %v, want 500", got)
	}
	if got := l.requestBucket.Available(); got < 395 || got > 405 {
		t.Errorf("request Available = %v, want ~400", got)
	}
}

func TestLimiterUpdateLimits_ZeroResetDefaultsTo60s(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})

	// reset=0 → should default to 60s, not panic or divide-by-zero
	l.UpdateLimits(60, 30, 0, 10000, 5000, 0)

	// Should not have panicked; capacity updated
	if got := l.requestBucket.Capacity(); got != 60 {
		t.Errorf("request Capacity = %v, want 60", got)
	}
}

func TestLimiterUpdateLimits_NegativeRemainingSkipsSync(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	// Drain some
	l.TryAcquire(0)

	availBefore := l.requestBucket.Available()
	// reqRemaining = -1 (header absent) → Sync should be skipped
	l.UpdateLimits(0, -1, 60*time.Second, 0, -1, 60*time.Second)

	availAfter := l.requestBucket.Available()
	// Capacity unchanged (reqLimit=0 → skip), available unchanged (remaining=-1 → skip)
	if availAfter < availBefore-1 || availAfter > availBefore+1 {
		t.Errorf("Available changed from %v to %v with -1 remaining (should skip)", availBefore, availAfter)
	}
}

func TestLimiterUpdateLimits_ZeroRemainingSyncs(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	// reqRemaining = 0 is a legitimate "quota exhausted" → should Sync to 0
	l.UpdateLimits(100, 0, 60*time.Second, 10000, 0, 60*time.Second)

	if got := l.requestBucket.Available(); got > 1 {
		t.Errorf("Available = %v, want ~0 (synced to exhausted quota)", got)
	}
}

// ===========================================================================
// Limiter — ReturnTokens
// ===========================================================================

func TestLimiterReturnTokens_DisabledIsNoOp(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	// ReturnTokens on a disabled limiter must be a no-op — no panic, no state change.
	l.ReturnTokens(1, 100)

	s := l.Stats()
	if s.TotalRequests != 0 {
		t.Errorf("TotalRequests = %d, want 0 (disabled)", s.TotalRequests)
	}
}

func TestLimiterReturnTokens_ReturnsBoth(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	// Consume some
	l.TryAcquire(100)

	availReqBefore := l.requestBucket.Available()
	availTokBefore := l.tokenBucket.Available()

	l.ReturnTokens(1, 100)

	if got := l.requestBucket.Available(); got <= availReqBefore {
		t.Errorf("request Available = %v, want > %v (returned)", got, availReqBefore)
	}
	if got := l.tokenBucket.Available(); got <= availTokBefore {
		t.Errorf("token Available = %v, want > %v (returned)", got, availTokBefore)
	}
}

func TestLimiterReturnTokens_ZeroValues(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	availBefore := l.requestBucket.Available()
	// Both 0 → no-op
	l.ReturnTokens(0, 0)
	if got := l.requestBucket.Available(); got != availBefore {
		t.Errorf("Available changed from %v to %v with zero returns", availBefore, got)
	}
}

// ===========================================================================
// Limiter — SetEnabled / Stats / Reset
// ===========================================================================

func TestLimiterSetEnabled_TogglesState(t *testing.T) {
	l := NewLimiter(Config{Enabled: true})
	if !l.isEnabled() {
		t.Error("should be enabled initially")
	}
	l.SetEnabled(false)
	if l.isEnabled() {
		t.Error("should be disabled after SetEnabled(false)")
	}
	// Now Acquire should be a no-op
	if err := l.Acquire(100); err != nil {
		t.Errorf("Acquire after disable: %v", err)
	}
}

func TestLimiterReset_ClearsAll(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   10000,
		BurstSize:         10,
	})
	// Generate some activity
	l.TryAcquire(100)
	l.RecordUsage(500)
	l.TryAcquire(100) // might be blocked

	l.Reset()

	s := l.Stats()
	if s.TotalRequests != 0 {
		t.Errorf("TotalRequests after Reset = %d, want 0", s.TotalRequests)
	}
	if s.BlockedRequests != 0 {
		t.Errorf("BlockedRequests after Reset = %d, want 0", s.BlockedRequests)
	}
	if s.TotalTokens != 0 {
		t.Errorf("TotalTokens after Reset = %d, want 0", s.TotalTokens)
	}
}

func TestLimiterRecordUsage_Accumulates(t *testing.T) {
	l := NewLimiter(Config{Enabled: true, RequestsPerMinute: 60, TokensPerMinute: 10000, BurstSize: 10})
	l.RecordUsage(100)
	l.RecordUsage(200)
	if got := l.Stats().TotalTokens; got != 300 {
		t.Errorf("TotalTokens = %d, want 300", got)
	}
}

// ===========================================================================
// Limiter — Concurrency safety (race detector)
// ===========================================================================

func TestLimiterConcurrent_TryAcquire(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 6000, // high limit
		TokensPerMinute:   1000000,
		BurstSize:         100,
	})

	var wg sync.WaitGroup
	var successes atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.TryAcquire(10) {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()

	// Should not panic or race; some succeed, some may be blocked
	if successes.Load() == 0 {
		t.Error("expected at least some successful TryAcquire calls")
	}
}

// ===========================================================================
// DefaultConfig
// ===========================================================================

func TestDefaultConfig_Values(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Enabled {
		t.Error("should be enabled by default")
	}
	if cfg.RequestsPerMinute != 60 {
		t.Errorf("RequestsPerMinute = %d, want 60", cfg.RequestsPerMinute)
	}
	if cfg.TokensPerMinute != 1000000 {
		t.Errorf("TokensPerMinute = %d, want 1000000", cfg.TokensPerMinute)
	}
	if cfg.BurstSize != 10 {
		t.Errorf("BurstSize = %d, want 10", cfg.BurstSize)
	}
}
