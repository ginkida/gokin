package app

import (
	"sync"
	"time"
)

// healthToastCooldown is how long the per-server rate limiter waits before
// emitting another UI toast for the same MCP server. A flapping server
// (unhealthy → healthy → unhealthy → healthy within tens of seconds) would
// otherwise cascade a wall of alternating toasts; the first flip is
// informative, subsequent rapid flips just add noise.
const healthToastCooldown = 30 * time.Second

// healthToastLimiter rate-limits UI notifications for MCP server health
// changes. Use ShouldEmit to decide whether to surface the current flip.
type healthToastLimiter struct {
	mu       sync.Mutex
	lastFlip map[string]time.Time
	cooldown time.Duration
}

// newHealthToastLimiter creates a limiter with the default cooldown.
func newHealthToastLimiter() *healthToastLimiter {
	return &healthToastLimiter{
		lastFlip: make(map[string]time.Time),
		cooldown: healthToastCooldown,
	}
}

// ShouldEmit returns true if a toast for this server should be emitted now,
// updating the internal timestamp. Returns false if the cooldown hasn't
// elapsed since the last emit for the same server.
func (l *healthToastLimiter) ShouldEmit(name string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, seen := l.lastFlip[name]; seen && now.Sub(last) < l.cooldown {
		return false
	}
	l.lastFlip[name] = now
	return true
}
