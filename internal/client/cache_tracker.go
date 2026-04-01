package client

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// CacheTracker detects prompt cache breaks by tracking hash changes
// of the system prompt, tools, and message prefix between API calls.
// This helps diagnose performance regressions caused by cache invalidation.
type CacheTracker struct {
	mu sync.RWMutex

	prevSystemHash string
	prevToolsHash  string

	// Cumulative metrics
	TotalCreationTokens int64  // Total tokens written to cache
	TotalReadTokens     int64  // Total tokens reused from cache
	CacheBreaks         int    // Number of detected cache invalidations
	LastBreakReason     string // Reason for last break
	LastBreakTime       time.Time
}

// NewCacheTracker creates a new cache break tracker.
func NewCacheTracker() *CacheTracker {
	return &CacheTracker{}
}

// CacheBreakType describes what caused a cache invalidation.
type CacheBreakType string

const (
	CacheBreakNone          CacheBreakType = ""
	CacheBreakSystemPrompt  CacheBreakType = "system_prompt_changed"
	CacheBreakToolsChanged  CacheBreakType = "tools_changed"
)

// RecordState records the current state of cacheable components.
// Returns the type of cache break detected (empty string if none).
func (t *CacheTracker) RecordState(systemPrompt string, toolsJSON string) CacheBreakType {
	t.mu.Lock()
	defer t.mu.Unlock()

	sysHash := quickHash(systemPrompt)
	toolHash := quickHash(toolsJSON)

	var breakType CacheBreakType

	if t.prevSystemHash != "" { // Skip first call
		if sysHash != t.prevSystemHash {
			breakType = CacheBreakSystemPrompt
		} else if toolHash != t.prevToolsHash {
			breakType = CacheBreakToolsChanged
		}
	}

	t.prevSystemHash = sysHash
	t.prevToolsHash = toolHash

	if breakType != "" {
		t.CacheBreaks++
		t.LastBreakReason = string(breakType)
		t.LastBreakTime = time.Now()
	}

	return breakType
}

// RecordUsage updates cumulative cache token metrics from an API response.
func (t *CacheTracker) RecordUsage(creationTokens, readTokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.TotalCreationTokens += int64(creationTokens)
	t.TotalReadTokens += int64(readTokens)
}

// CacheEfficiency returns the ratio of reused tokens vs total cached tokens.
// Returns 0 if no caching activity has occurred.
func (t *CacheTracker) CacheEfficiency() float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	total := t.TotalCreationTokens + t.TotalReadTokens
	if total == 0 {
		return 0
	}
	return float64(t.TotalReadTokens) / float64(total)
}

// GetStats returns a snapshot of cache tracking statistics.
func (t *CacheTracker) GetStats() CacheStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return CacheStats{
		TotalCreationTokens: t.TotalCreationTokens,
		TotalReadTokens:     t.TotalReadTokens,
		CacheBreaks:         t.CacheBreaks,
		LastBreakReason:     t.LastBreakReason,
		LastBreakTime:       t.LastBreakTime,
		Efficiency:          t.CacheEfficiency(),
	}
}

// CacheStats holds a snapshot of cache tracking statistics.
type CacheStats struct {
	TotalCreationTokens int64
	TotalReadTokens     int64
	CacheBreaks         int
	LastBreakReason     string
	LastBreakTime       time.Time
	Efficiency          float64
}

func quickHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8]) // 16 hex chars is enough for change detection
}
