package app

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"time"
)

const (
	maxAutoRateLimitRetries = 2
)

var autoRateLimitDelays = []time.Duration{
	30 * time.Second,
	75 * time.Second,
}

func rateLimitRetryKey(message string) string {
	msg := strings.TrimSpace(message)
	sum := sha1.Sum([]byte(msg))
	return hex.EncodeToString(sum[:])
}

func (a *App) scheduleRateLimitAutoRetry(message string) (attempt int, delay time.Duration, scheduled bool) {
	key := rateLimitRetryKey(message)

	a.rateLimitRetryMu.Lock()
	defer a.rateLimitRetryMu.Unlock()

	current := a.rateLimitRetryCount[key]
	if current >= maxAutoRateLimitRetries {
		return current, 0, false
	}

	next := current + 1
	a.rateLimitRetryCount[key] = next

	if next-1 < len(autoRateLimitDelays) {
		delay = autoRateLimitDelays[next-1]
	} else {
		delay = autoRateLimitDelays[len(autoRateLimitDelays)-1]
	}
	return next, delay, true
}

func (a *App) clearRateLimitRetry(message string) {
	key := rateLimitRetryKey(message)
	a.rateLimitRetryMu.Lock()
	delete(a.rateLimitRetryCount, key)
	a.rateLimitRetryMu.Unlock()
}
