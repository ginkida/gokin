package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"gokin/internal/logging"
)

// CacheEntry represents a cached tool result with metadata.
type CacheEntry struct {
	Result    ToolResult
	Timestamp time.Time
	HitCount  int
	ToolName  string
	Args      map[string]any
}

// ToolResultCache provides LRU caching for tool results.
type ToolResultCache struct {
	entries    map[string]*CacheEntry
	lruList    []string // Simple LRU tracking
	maxEntries int
	ttl        time.Duration
	mu         sync.RWMutex

	// Statistics
	hits   int
	misses int
}

// CacheConfig holds cache configuration.
type CacheConfig struct {
	MaxEntries int           // Maximum number of cache entries (default: 100)
	TTL        time.Duration // Time-to-live for cache entries (default: 5 minutes)
}

// DefaultCacheConfig returns the default cache configuration.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		MaxEntries: 100,
		TTL:        5 * time.Minute,
	}
}

// NewToolResultCache creates a new tool result cache.
func NewToolResultCache(config CacheConfig) *ToolResultCache {
	if config.MaxEntries <= 0 {
		config.MaxEntries = 100
	}
	if config.TTL <= 0 {
		config.TTL = 5 * time.Minute
	}

	return &ToolResultCache{
		entries:    make(map[string]*CacheEntry),
		lruList:    make([]string, 0, config.MaxEntries),
		maxEntries: config.MaxEntries,
		ttl:        config.TTL,
	}
}

// IsCacheable returns true if a tool should be cached.
// Read-only and git-query tools are cached.
func (c *ToolResultCache) IsCacheable(toolName string) bool {
	cacheableTools := map[string]bool{
		"read":       true, // File content
		"glob":       true, // File listings
		"grep":       true, // Search results
		"tree":       true, // Directory structure
		"env":        true, // Environment variables
		"list_dir":   true, // Directory contents
		"git_log":    true, // Git history
		"git_blame":  true, // Blame info
		"git_status": true, // Git status (short TTL, invalidated by writes)
		"git_diff":   true, // Git diff (short TTL, invalidated by writes)
		"code_graph": true, // Code structure analysis
	}

	return cacheableTools[toolName]
}

// Get retrieves a cached result if available and not expired.
// Note: Uses full Lock (not RLock) because we mutate state (HitCount++, hits++, updateLRU).
func (c *ToolResultCache) Get(toolName string, args map[string]any) (ToolResult, bool) {
	if !c.IsCacheable(toolName) {
		return ToolResult{}, false
	}

	key := c.makeKey(toolName, args)

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[key]
	if !exists {
		c.misses++
		return ToolResult{}, false
	}

	// Check if expired
	if time.Since(entry.Timestamp) > c.ttl {
		// Entry expired, will be cleaned up by next Put
		c.misses++
		return ToolResult{}, false
	}

	// Update LRU and hit count
	c.updateLRU(key)
	entry.HitCount++
	c.hits++

	logging.Debug("cache hit",
		"tool", toolName,
		"key", truncKey(key),
		"hits", entry.HitCount)

	return entry.Result, true
}

// Put stores a tool result in the cache.
func (c *ToolResultCache) Put(toolName string, args map[string]any, result ToolResult) {
	if !c.IsCacheable(toolName) {
		return
	}

	// Don't cache errors
	if result.Error != "" {
		return
	}

	// Don't cache very large results (>100KB)
	if len(result.Content) > 100*1024 {
		logging.Debug("skipping cache for large result",
			"tool", toolName,
			"size", len(result.Content))
		return
	}

	key := c.makeKey(toolName, args)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if we need to evict
	if len(c.entries) >= c.maxEntries {
		c.evictLRU()
	}

	entry := &CacheEntry{
		Result:    result,
		Timestamp: time.Now(),
		HitCount:  0,
		ToolName:  toolName,
		Args:      args,
	}

	c.entries[key] = entry
	c.lruList = append(c.lruList, key)

	logging.Debug("cached result",
		"tool", toolName,
		"key", truncKey(key),
		"size", len(result.Content),
		"total_entries", len(c.entries))
}

// Invalidate removes a specific entry from cache.
func (c *ToolResultCache) Invalidate(toolName string, args map[string]any) {
	key := c.makeKey(toolName, args)

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; exists {
		delete(c.entries, key)
		c.removeLRU(key)

		logging.Debug("cache invalidated",
			"tool", toolName,
			"key", truncKey(key))
	}
}

// InvalidateByTool removes all entries for a specific tool.
func (c *ToolResultCache) InvalidateByTool(toolName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	keysToRemove := []string{}
	for key, entry := range c.entries {
		if entry.ToolName == toolName {
			keysToRemove = append(keysToRemove, key)
		}
	}

	for _, key := range keysToRemove {
		delete(c.entries, key)
		c.removeLRU(key)
	}

	logging.Debug("cache invalidated by tool",
		"tool", toolName,
		"removed", len(keysToRemove))
}

// InvalidateByFile removes all cache entries related to a specific file.
// Useful for write operations to invalidate cached read results.
func (c *ToolResultCache) InvalidateByFile(filePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	keysToRemove := []string{}
	for key, entry := range c.entries {
		// Check if args contain the file path
		if entry.Args != nil {
			if path, ok := entry.Args["file_path"].(string); ok && path == filePath {
				keysToRemove = append(keysToRemove, key)
			}
			if path, ok := entry.Args["path"].(string); ok && path == filePath {
				keysToRemove = append(keysToRemove, key)
			}
		}
	}

	for _, key := range keysToRemove {
		delete(c.entries, key)
		c.removeLRU(key)
	}

	if len(keysToRemove) > 0 {
		logging.Debug("cache invalidated by file",
			"file", filePath,
			"removed", len(keysToRemove))
	}
}

// Clear removes all entries from the cache.
func (c *ToolResultCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = make(map[string]*CacheEntry)
	c.lruList = make([]string, 0, c.maxEntries)
	c.hits = 0
	c.misses = 0

	logging.Debug("cache cleared")
}

// Cleanup removes expired entries from the cache.
func (c *ToolResultCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	keysToRemove := []string{}

	for key, entry := range c.entries {
		if now.Sub(entry.Timestamp) > c.ttl {
			keysToRemove = append(keysToRemove, key)
		}
	}

	for _, key := range keysToRemove {
		delete(c.entries, key)
		c.removeLRU(key)
	}

	if len(keysToRemove) > 0 {
		logging.Debug("cache cleanup",
			"expired", len(keysToRemove),
			"remaining", len(c.entries))
	}
}

// GetStats returns cache statistics.
func (c *ToolResultCache) GetStats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	totalRequests := c.hits + c.misses
	hitRate := 0.0
	if totalRequests > 0 {
		hitRate = float64(c.hits) / float64(totalRequests)
	}

	return CacheStats{
		Entries:    len(c.entries),
		Hits:       c.hits,
		Misses:     c.misses,
		HitRate:    hitRate,
		MaxEntries: c.maxEntries,
		TTL:        c.ttl,
	}
}

// CacheStats holds cache statistics.
type CacheStats struct {
	Entries    int           // Current number of entries
	Hits       int           // Total cache hits
	Misses     int           // Total cache misses
	HitRate    float64       // Cache hit rate (0.0 - 1.0)
	MaxEntries int           // Maximum entries
	TTL        time.Duration // Time-to-live
}

// truncKey safely truncates a cache key for logging.
func truncKey(key string) string {
	if len(key) > 8 {
		return key[:8] + "..."
	}
	return key
}

// makeKey creates a cache key from tool name and arguments.
func (c *ToolResultCache) makeKey(toolName string, args map[string]any) string {
	// Create a deterministic hash of the arguments
	keyData := fmt.Sprintf("%s:%v", toolName, args)

	// For grep/glob, include pattern + path for a correct key
	if toolName == "grep" || toolName == "glob" {
		pattern, _ := args["pattern"].(string)
		path, _ := args["path"].(string)
		keyData = fmt.Sprintf("%s:%s:%s:%v", toolName, pattern, path, args)
	}

	hash := sha256.Sum256([]byte(keyData))
	return toolName + ":" + hex.EncodeToString(hash[:])[:16]
}

// updateLRU updates the LRU list for a cache hit.
func (c *ToolResultCache) updateLRU(key string) {
	// Remove from current position, then add to end (most recently used)
	c.removeLRU(key)
	c.lruList = append(c.lruList, key)
}

// evictLRU removes the least recently used entry.
func (c *ToolResultCache) evictLRU() {
	if len(c.lruList) == 0 {
		return
	}

	// Remove first entry (least recently used)
	key := c.lruList[0]
	delete(c.entries, key)
	c.lruList = c.lruList[1:]

	logging.Debug("cache evicted LRU",
		"key", truncKey(key),
		"remaining", len(c.entries))
}

// removeLRU removes a key from the LRU list.
func (c *ToolResultCache) removeLRU(key string) {
	for i, k := range c.lruList {
		if k == key {
			c.lruList = append(c.lruList[:i], c.lruList[i+1:]...)
			return
		}
	}
}
