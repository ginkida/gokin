package ratelimit

import (
	"context"
	"sync"
	"time"
)

// TokenBucket implements a token bucket rate limiter.
type TokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	mu         sync.Mutex
}

// NewTokenBucket creates a new token bucket with the given parameters.
// maxTokens is the maximum number of tokens the bucket can hold.
// refillRate is the number of tokens added per second.
func NewTokenBucket(maxTokens float64, refillRate float64) *TokenBucket {
	return &TokenBucket{
		tokens:     maxTokens,
		maxTokens:  maxTokens,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// maxRefillSeconds caps the elapsed time used for token refill calculation.
// This prevents a burst of tokens after system sleep/wake: e.g. 1 hour of sleep
// would otherwise add 3600 * refillRate tokens instantly. With a 120s cap and
// typical maxTokens=10, the bucket simply fills to capacity without overflow.
const maxRefillSeconds = 120.0

// refill adds tokens based on elapsed time since last refill.
func (b *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > maxRefillSeconds {
		elapsed = maxRefillSeconds
	}
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now
}

// TryConsume attempts to consume the specified number of tokens.
// Returns true if successful, false if not enough tokens available.
func (b *TokenBucket) TryConsume(tokens float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refill()

	consumeAmount := tokens
	if consumeAmount > b.maxTokens {
		consumeAmount = b.maxTokens
	}

	if b.tokens >= consumeAmount {
		b.tokens -= consumeAmount
		return true
	}
	return false
}

// Consume blocks until the specified number of tokens are available.
func (b *TokenBucket) Consume(tokens float64) {
	for {
		b.mu.Lock()
		b.refill()

		consumeAmount := tokens
		if consumeAmount > b.maxTokens {
			consumeAmount = b.maxTokens
		}

		if b.tokens >= consumeAmount {
			b.tokens -= consumeAmount
			b.mu.Unlock()
			return
		}

		// Calculate wait time for required tokens
		deficit := consumeAmount - b.tokens
		if b.refillRate <= 0 {
			// Will never refill — allow immediately to avoid infinite hang
			b.tokens = 0
			b.mu.Unlock()
			return
		}
		waitTime := time.Duration(deficit/b.refillRate*1000) * time.Millisecond
		b.mu.Unlock()

		if waitTime < 10*time.Millisecond {
			waitTime = 10 * time.Millisecond
		}
		timer := time.NewTimer(waitTime)
		<-timer.C
		timer.Stop()
	}
}

// ConsumeWithTimeout attempts to consume tokens within the given timeout.
// Returns true if successful, false if timeout expired.
func (b *TokenBucket) ConsumeWithTimeout(tokens float64, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		b.mu.Lock()
		b.refill()

		consumeAmount := tokens
		if consumeAmount > b.maxTokens {
			consumeAmount = b.maxTokens
		}

		if b.tokens >= consumeAmount {
			b.tokens -= consumeAmount
			b.mu.Unlock()
			return true
		}

		// Calculate wait time for required tokens
		deficit := consumeAmount - b.tokens
		if b.refillRate <= 0 {
			b.tokens = 0
			b.mu.Unlock()
			return true
		}
		waitTime := time.Duration(deficit/b.refillRate*1000) * time.Millisecond
		b.mu.Unlock()

		if waitTime < 10*time.Millisecond {
			waitTime = 10 * time.Millisecond
		}
		wait := time.NewTimer(waitTime)
		select {
		case <-wait.C:
			// Try again
		case <-deadline.C:
			wait.Stop()
			return false
		}
	}
}

// ConsumeWithContext blocks until tokens are available or context is cancelled.
func (b *TokenBucket) ConsumeWithContext(ctx context.Context, tokens float64) error {
	for {
		b.mu.Lock()
		b.refill()

		consumeAmount := tokens
		if consumeAmount > b.maxTokens {
			consumeAmount = b.maxTokens
		}

		if b.tokens >= consumeAmount {
			b.tokens -= consumeAmount
			b.mu.Unlock()
			return nil
		}
		deficit := consumeAmount - b.tokens
		if b.refillRate <= 0 {
			b.tokens = 0
			b.mu.Unlock()
			return nil
		}
		waitTime := time.Duration(deficit/b.refillRate*1000) * time.Millisecond
		b.mu.Unlock()

		if waitTime < 10*time.Millisecond {
			waitTime = 10 * time.Millisecond
		}
		timer := time.NewTimer(waitTime)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// Available returns the current number of available tokens.
func (b *TokenBucket) Available() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refill()
	return b.tokens
}

// Reset resets the bucket to full capacity.
func (b *TokenBucket) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.tokens = b.maxTokens
	b.lastRefill = time.Now()
}

// Return returns tokens back to the bucket.
// This is useful when a request is cancelled or fails and the tokens should be released.
func (b *TokenBucket) Return(tokens float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.tokens += tokens
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
}
