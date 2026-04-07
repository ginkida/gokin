package ratelimit

import (
	"context"
	"testing"
	"time"
)

// --- TokenBucket tests ---

func TestTokenBucketTryConsume(t *testing.T) {
	b := NewTokenBucket(5, 10) // 5 max, 10/sec refill

	// Should succeed 5 times (all tokens available)
	for i := 0; i < 5; i++ {
		if !b.TryConsume(1) {
			t.Errorf("TryConsume should succeed on attempt %d", i+1)
		}
	}

	// 6th should fail (no tokens left)
	if b.TryConsume(1) {
		t.Error("TryConsume should fail when empty")
	}
}

func TestTokenBucketTryConsumeClampToMax(t *testing.T) {
	b := NewTokenBucket(5, 10)
	// Request more than max — should clamp to max and succeed
	if !b.TryConsume(100) {
		t.Error("TryConsume(100) should succeed (clamped to maxTokens=5)")
	}
	// Bucket should be empty now
	if b.TryConsume(1) {
		t.Error("bucket should be empty after consuming max")
	}
}

func TestTokenBucketAvailable(t *testing.T) {
	b := NewTokenBucket(10, 0) // no refill
	if a := b.Available(); a != 10 {
		t.Errorf("Available = %v, want 10", a)
	}
	b.TryConsume(3)
	if a := b.Available(); a != 7 {
		t.Errorf("Available = %v, want 7", a)
	}
}

func TestTokenBucketReset(t *testing.T) {
	b := NewTokenBucket(10, 0)
	b.TryConsume(10)
	if b.Available() != 0 {
		t.Error("should be empty")
	}
	b.Reset()
	if b.Available() != 10 {
		t.Errorf("after Reset, Available = %v, want 10", b.Available())
	}
}

func TestTokenBucketReturn(t *testing.T) {
	b := NewTokenBucket(10, 0)
	b.TryConsume(5)
	b.Return(3)
	if a := b.Available(); a != 8 {
		t.Errorf("after Return(3), Available = %v, want 8", a)
	}
	// Return beyond max should cap
	b.Return(100)
	if a := b.Available(); a != 10 {
		t.Errorf("Return beyond max: Available = %v, want 10", a)
	}
}

func TestTokenBucketConsume(t *testing.T) {
	b := NewTokenBucket(5, 1000) // fast refill
	b.TryConsume(5)              // drain
	// Consume should block briefly then succeed
	done := make(chan bool, 1)
	go func() {
		b.Consume(1)
		done <- true
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Error("Consume blocked too long")
	}
}

func TestTokenBucketConsumeZeroRefill(t *testing.T) {
	b := NewTokenBucket(5, 0) // zero refill
	b.TryConsume(5)           // drain

	// With zero refill, Consume should not hang — allows immediately
	done := make(chan bool, 1)
	go func() {
		b.Consume(1)
		done <- true
	}()

	select {
	case <-done:
		// ok — zero refill rate allows immediately to avoid hang
	case <-time.After(1 * time.Second):
		t.Error("Consume with zero refill should not hang")
	}
}

func TestTokenBucketConsumeWithTimeout(t *testing.T) {
	b := NewTokenBucket(5, 1000) // fast refill
	b.TryConsume(5)

	if !b.ConsumeWithTimeout(1, 2*time.Second) {
		t.Error("should succeed with fast refill and generous timeout")
	}

	// Very short timeout with empty bucket and slow refill
	b2 := NewTokenBucket(5, 0.001) // very slow refill
	b2.TryConsume(5)
	if b2.ConsumeWithTimeout(1, 10*time.Millisecond) {
		t.Error("should fail with very slow refill and short timeout")
	}
}

func TestTokenBucketConsumeWithContext(t *testing.T) {
	b := NewTokenBucket(5, 1000)
	b.TryConsume(5)

	ctx := context.Background()
	err := b.ConsumeWithContext(ctx, 1)
	if err != nil {
		t.Errorf("should succeed: %v", err)
	}

	// Cancelled context
	b2 := NewTokenBucket(5, 0.001) // very slow
	b2.TryConsume(5)
	ctx2, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = b2.ConsumeWithContext(ctx2, 1)
	if err == nil {
		t.Error("should fail with cancelled context")
	}
}

func TestTokenBucketRefillCap(t *testing.T) {
	b := NewTokenBucket(10, 100)
	// Manually set lastRefill far in the past
	b.mu.Lock()
	b.lastRefill = time.Now().Add(-1 * time.Hour)
	b.tokens = 0
	b.mu.Unlock()

	// After refill, should not exceed maxTokens even with maxRefillSeconds cap
	avail := b.Available()
	if avail > 10 {
		t.Errorf("Available = %v, should not exceed maxTokens=10", avail)
	}
}

// --- Limiter tests ---

func TestDefaultConfig(t *testing.T) {
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

func TestLimiterDisabled(t *testing.T) {
	l := NewLimiter(Config{Enabled: false})
	if err := l.Acquire(1000); err != nil {
		t.Errorf("disabled limiter should not error: %v", err)
	}
	if !l.TryAcquire(1000) {
		t.Error("disabled limiter TryAcquire should return true")
	}
}

func TestLimiterTryAcquire(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   1000000,
		BurstSize:         3,
	})

	// Should succeed for burst size
	for i := 0; i < 3; i++ {
		if !l.TryAcquire(0) {
			t.Errorf("TryAcquire should succeed on attempt %d", i+1)
		}
	}

	// Next should fail (burst exhausted)
	if l.TryAcquire(0) {
		t.Error("TryAcquire should fail when burst exhausted")
	}
}

func TestLimiterAcquireWithTimeout(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   1000000,
		BurstSize:         1,
	})

	// First should succeed
	if err := l.AcquireWithTimeout(0, time.Second); err != nil {
		t.Errorf("first AcquireWithTimeout should succeed: %v", err)
	}

	// Second should fail quickly with short timeout
	err := l.AcquireWithTimeout(0, 10*time.Millisecond)
	if err == nil {
		t.Error("should fail when burst exhausted with short timeout")
	}
}

func TestLimiterAcquireWithContext(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 6000, // fast refill
		TokensPerMinute:   1000000,
		BurstSize:         1,
	})

	l.TryAcquire(0) // drain burst

	// Increased timeout to 2s to accommodate the 1s safety delay in Danger Zone (< 5%)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := l.AcquireWithContext(ctx, 0)
	if err != nil {
		t.Errorf("should succeed with fast refill (including safety delay): %v", err)
	}
}

func TestLimiterStats(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   1000000,
		BurstSize:         5,
	})

	l.TryAcquire(100)
	l.TryAcquire(100)
	l.RecordUsage(200)

	stats := l.Stats()
	if !stats.Enabled {
		t.Error("should be enabled")
	}
	if stats.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", stats.TotalRequests)
	}
	if stats.TotalTokens != 200 {
		t.Errorf("TotalTokens = %d, want 200", stats.TotalTokens)
	}
}

func TestLimiterReturnTokens(t *testing.T) {
	l := NewLimiter(Config{
		Enabled:           true,
		RequestsPerMinute: 60,
		TokensPerMinute:   1000000,
		BurstSize:         2,
	})

	l.TryAcquire(0)
	l.TryAcquire(0)
	// Burst exhausted
	if l.TryAcquire(0) {
		t.Error("should be exhausted")
	}

	// Return a token
	l.ReturnTokens(1, 0)
	if !l.TryAcquire(0) {
		t.Error("should succeed after ReturnTokens")
	}
}

func TestLimiterSetEnabled(t *testing.T) {
	l := NewLimiter(Config{Enabled: true, RequestsPerMinute: 60, BurstSize: 1})
	l.TryAcquire(0)
	if l.TryAcquire(0) {
		t.Error("should fail when enabled and burst exhausted")
	}
	l.SetEnabled(false)
	if !l.TryAcquire(0) {
		t.Error("should succeed when disabled")
	}
}

func TestLimiterReset(t *testing.T) {
	l := NewLimiter(Config{Enabled: true, RequestsPerMinute: 60, BurstSize: 2, TokensPerMinute: 1000000})
	l.TryAcquire(0)
	l.TryAcquire(0)
	l.RecordUsage(100)
	l.Reset()

	stats := l.Stats()
	if stats.TotalRequests != 0 {
		t.Errorf("after Reset, TotalRequests = %d", stats.TotalRequests)
	}
	if stats.TotalTokens != 0 {
		t.Errorf("after Reset, TotalTokens = %d", stats.TotalTokens)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	// 20 chars -> ~5 tokens
	if got := EstimateTokens("12345678901234567890"); got != 5 {
		t.Errorf("20 chars = %d, want 5", got)
	}
}

func TestEstimateTokensFromContents(t *testing.T) {
	// 3 contents * 100 avg length / 4 = 75
	if got := EstimateTokensFromContents(3, 100); got != 75 {
		t.Errorf("3*100 = %d, want 75", got)
	}
}
