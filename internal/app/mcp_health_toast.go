package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gokin/internal/mcp"
	"gokin/internal/ui"
)

// Runtime managers can gain servers after creation, so monitoring starts even
// when the manager is empty. The caller supplies the app lifetime and config
// snapshot to keep this usable while EnableMCP holds a.mu.
func (a *App) wireMCPManagerHealth(manager *mcp.Manager, ctx context.Context, interval time.Duration) {
	healthToasts := newHealthToastLimiter()
	manager.SetHealthChangeCallback(func(name string, healthy bool) {
		if !healthToasts.ShouldEmit(name, time.Now()) {
			return
		}
		if healthy {
			a.safeSendToProgram(ui.StatusUpdateMsg{
				Type:    ui.StatusStreamResume,
				Message: fmt.Sprintf("MCP server %q reconnected", name),
			})
		} else {
			a.safeSendToProgram(ui.StatusUpdateMsg{
				Type:    ui.StatusRecoverableError,
				Message: fmt.Sprintf("MCP server %q disconnected", name),
			})
		}
	})
	if interval > 0 {
		manager.StartHealthCheck(ctx, interval)
	}
}

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
