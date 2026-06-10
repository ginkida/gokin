package cache

import (
	"testing"
	"time"
)

// TestNewSearchCache_NonPositiveTTLClamped pins that a config zero/negative TTL
// (`cache.ttl: 0s`) is clamped to a sane default so the LRU isn't an
// instant-expiry no-op and backgroundCleanup's NewTicker(ttl/2) can't be 0.
func TestNewSearchCache_NonPositiveTTLClamped(t *testing.T) {
	for _, ttl := range []time.Duration{0, -1 * time.Second} {
		c := NewSearchCache(10, ttl)
		c.SetGrep("k", GrepResult{})
		if _, ok := c.GetGrep("k"); !ok {
			t.Fatalf("ttl=%v: entry expired immediately — clamp not applied (cache is a no-op)", ttl)
		}
	}
}

// TestNewSearchCache_TinyTTLNoTickerPanic pins that a degenerate sub-2ns TTL
// (where ttl/2 rounds to 0) does NOT panic backgroundCleanup's ticker. A 1ns
// TTL legitimately expires entries immediately; only the panic must be avoided.
func TestNewSearchCache_TinyTTLNoTickerPanic(t *testing.T) {
	c := NewSearchCache(10, 1*time.Nanosecond) // must not panic during construction
	c.SetGrep("k", GrepResult{})
	_, _ = c.GetGrep("k") // expiry behavior is intentionally unasserted
}
