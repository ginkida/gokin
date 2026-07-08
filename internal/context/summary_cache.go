package context

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"gokin/internal/stable"

	"google.golang.org/genai"
)

// CachedSummary represents a cached summary with metadata.
type CachedSummary struct {
	Summary      *genai.Content
	TokenCount   int
	CreatedAt    time.Time
	LastUsedAt   time.Time
	MessageHash  string // Hash of the messages that were summarized
	MessageRange struct {
		Start int
		End   int
	}
}

// SummaryCache caches generated summaries to avoid regeneration.
type SummaryCache struct {
	mu       sync.RWMutex
	cache    map[string]*CachedSummary // messageHash -> summary
	lruList  []string                  // Order of recent use (front = most recent)
	maxCache int
	ttl      time.Duration // Time-to-live for cache entries
	hits     int64
	misses   int64
}

// NewSummaryCache creates a new summary cache.
func NewSummaryCache(maxEntries int, ttl time.Duration) *SummaryCache {
	if maxEntries < 0 {
		maxEntries = 0
	}
	return &SummaryCache{
		cache:    make(map[string]*CachedSummary),
		lruList:  make([]string, 0, maxEntries),
		maxCache: maxEntries,
		ttl:      ttl,
	}
}

// Get retrieves a cached summary if available and not expired.
// Note: Uses full Lock (not RLock) because we mutate LastUsedAt.
// Returns a COPY of the cached entry to prevent use-after-free if the entry is evicted.
func (c *SummaryCache) Get(messageHash string) (*CachedSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.cache[messageHash]
	if !ok {
		c.misses++
		return nil, false
	}

	// Check TTL
	if c.ttl > 0 && time.Since(entry.CreatedAt) > c.ttl {
		delete(c.cache, messageHash)
		c.removeFromLRU(messageHash)
		c.misses++
		return nil, false
	}

	c.hits++

	// Update last used time
	entry.LastUsedAt = time.Now()

	// Move to front of LRU list
	for i, key := range c.lruList {
		if key == messageHash {
			c.lruList = append(c.lruList[:i], c.lruList[i+1:]...)
			c.lruList = append([]string{messageHash}, c.lruList...)
			break
		}
	}

	// Return a COPY to prevent use-after-free when entry is evicted
	result := &CachedSummary{
		Summary:     cloneContent(entry.Summary),
		TokenCount:  entry.TokenCount,
		CreatedAt:   entry.CreatedAt,
		LastUsedAt:  entry.LastUsedAt,
		MessageHash: entry.MessageHash,
	}
	result.MessageRange.Start = entry.MessageRange.Start
	result.MessageRange.End = entry.MessageRange.End

	return result, true
}

// Put stores a summary in the cache.
func (c *SummaryCache) Put(messageHash string, summary *genai.Content, tokenCount int, startIdx, endIdx int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.maxCache <= 0 {
		return
	}

	// Check if already exists
	if _, ok := c.cache[messageHash]; ok {
		return
	}

	// Evict oldest if at capacity
	if len(c.cache) >= c.maxCache {
		c.evictOldest()
	}

	now := time.Now()
	entry := &CachedSummary{
		Summary:     cloneContent(summary),
		TokenCount:  tokenCount,
		CreatedAt:   now,
		LastUsedAt:  now,
		MessageHash: messageHash,
	}
	entry.MessageRange.Start = startIdx
	entry.MessageRange.End = endIdx

	c.cache[messageHash] = entry
	c.lruList = append([]string{messageHash}, c.lruList...)
}

// Invalidate removes a cache entry.
func (c *SummaryCache) Invalidate(messageHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.cache, messageHash)
	c.removeFromLRU(messageHash)
}

// Clear removes all entries from the cache.
func (c *SummaryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[string]*CachedSummary)
	c.lruList = make([]string, 0, c.maxCache)
	c.hits = 0
	c.misses = 0
}

// evictOldest removes the least recently used entry.
func (c *SummaryCache) evictOldest() {
	if len(c.lruList) == 0 {
		return
	}

	// Remove last (oldest) entry
	oldest := c.lruList[len(c.lruList)-1]
	delete(c.cache, oldest)
	c.lruList = c.lruList[:len(c.lruList)-1]
}

func (c *SummaryCache) removeFromLRU(messageHash string) {
	for i, key := range c.lruList {
		if key == messageHash {
			c.lruList = append(c.lruList[:i], c.lruList[i+1:]...)
			return
		}
	}
}

// Size returns the current cache size.
func (c *SummaryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// GetStats returns cache statistics.
type CacheStats struct {
	Size    int
	Hits    int64
	Misses  int64
	HitRate float64
	TTL     time.Duration
	MaxSize int
}

// GetStats returns cache statistics.
func (c *SummaryCache) GetStats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	total := c.hits + c.misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(c.hits) / float64(total)
	}

	return CacheStats{
		Size:    len(c.cache),
		Hits:    c.hits,
		Misses:  c.misses,
		HitRate: hitRate,
		TTL:     c.ttl,
		MaxSize: c.maxCache,
	}
}

// HashMessages creates a hash of messages for cache key.
func HashMessages(messages []*genai.Content) string {
	h := sha256.New()
	for _, msg := range messages {
		if msg == nil {
			continue
		}

		writeHashString(h, string(msg.Role))
		for _, part := range msg.Parts {
			if part == nil {
				continue
			}

			if part.Text != "" {
				// For text, hash first 500 chars to avoid huge computations
				text := part.Text
				if runes := []rune(text); len(runes) > 500 {
					text = string(runes[:500])
				}
				writeHashString(h, text)
			}
			if part.FunctionCall != nil {
				writeHashString(h, part.FunctionCall.Name)
				writeHashString(h, stable.EncodeMap(part.FunctionCall.Args))
			}
			if part.FunctionResponse != nil {
				writeHashString(h, part.FunctionResponse.Name)
				writeHashString(h, stable.EncodeMap(part.FunctionResponse.Response))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashString(h hashWriter, s string) {
	var buf [20]byte // max int64 decimal = 20 digits
	h.Write(strconv.AppendInt(buf[:0], int64(len(s)), 10))
	h.Write([]byte(":"))
	h.Write([]byte(s))
}

func cloneContent(content *genai.Content) *genai.Content {
	if content == nil {
		return nil
	}
	clone := &genai.Content{
		Role:  content.Role,
		Parts: cloneParts(content.Parts),
	}
	return clone
}

func cloneParts(parts []*genai.Part) []*genai.Part {
	if parts == nil {
		return nil
	}
	clone := make([]*genai.Part, len(parts))
	for i, part := range parts {
		clone[i] = clonePart(part)
	}
	return clone
}

func clonePart(part *genai.Part) *genai.Part {
	if part == nil {
		return nil
	}
	clone := *part
	clone.ThoughtSignature = cloneBytes(part.ThoughtSignature)
	clone.MediaResolution = clonePartMediaResolution(part.MediaResolution)
	clone.CodeExecutionResult = cloneCodeExecutionResult(part.CodeExecutionResult)
	clone.ExecutableCode = cloneExecutableCode(part.ExecutableCode)
	clone.FileData = cloneFileData(part.FileData)
	clone.InlineData = cloneBlob(part.InlineData)
	clone.VideoMetadata = cloneVideoMetadata(part.VideoMetadata)
	if part.FunctionCall != nil {
		clone.FunctionCall = cloneFunctionCall(part.FunctionCall)
	}
	if part.FunctionResponse != nil {
		clone.FunctionResponse = cloneFunctionResponse(part.FunctionResponse)
	}
	return &clone
}

func clonePartMediaResolution(v *genai.PartMediaResolution) *genai.PartMediaResolution {
	if v == nil {
		return nil
	}
	clone := *v
	if v.NumTokens != nil {
		numTokens := *v.NumTokens
		clone.NumTokens = &numTokens
	}
	return &clone
}

func cloneCodeExecutionResult(v *genai.CodeExecutionResult) *genai.CodeExecutionResult {
	if v == nil {
		return nil
	}
	clone := *v
	return &clone
}

func cloneExecutableCode(v *genai.ExecutableCode) *genai.ExecutableCode {
	if v == nil {
		return nil
	}
	clone := *v
	return &clone
}

func cloneFileData(v *genai.FileData) *genai.FileData {
	if v == nil {
		return nil
	}
	clone := *v
	return &clone
}

func cloneBlob(v *genai.Blob) *genai.Blob {
	if v == nil {
		return nil
	}
	clone := *v
	clone.Data = cloneBytes(v.Data)
	return &clone
}

func cloneVideoMetadata(v *genai.VideoMetadata) *genai.VideoMetadata {
	if v == nil {
		return nil
	}
	clone := *v
	if v.FPS != nil {
		fps := *v.FPS
		clone.FPS = &fps
	}
	return &clone
}

func cloneFunctionCall(v *genai.FunctionCall) *genai.FunctionCall {
	if v == nil {
		return nil
	}
	clone := *v
	clone.Args = stable.CloneMap(v.Args)
	clone.PartialArgs = clonePartialArgs(v.PartialArgs)
	if v.WillContinue != nil {
		willContinue := *v.WillContinue
		clone.WillContinue = &willContinue
	}
	return &clone
}

func clonePartialArgs(args []*genai.PartialArg) []*genai.PartialArg {
	if args == nil {
		return nil
	}
	clone := make([]*genai.PartialArg, len(args))
	for i, arg := range args {
		if arg == nil {
			continue
		}
		argClone := *arg
		if arg.NumberValue != nil {
			numberValue := *arg.NumberValue
			argClone.NumberValue = &numberValue
		}
		if arg.BoolValue != nil {
			boolValue := *arg.BoolValue
			argClone.BoolValue = &boolValue
		}
		if arg.WillContinue != nil {
			willContinue := *arg.WillContinue
			argClone.WillContinue = &willContinue
		}
		clone[i] = &argClone
	}
	return clone
}

func cloneFunctionResponse(v *genai.FunctionResponse) *genai.FunctionResponse {
	if v == nil {
		return nil
	}
	clone := *v
	clone.Response = stable.CloneMap(v.Response)
	clone.Parts = cloneFunctionResponseParts(v.Parts)
	if v.WillContinue != nil {
		willContinue := *v.WillContinue
		clone.WillContinue = &willContinue
	}
	return &clone
}

func cloneFunctionResponseParts(parts []*genai.FunctionResponsePart) []*genai.FunctionResponsePart {
	if parts == nil {
		return nil
	}
	clone := make([]*genai.FunctionResponsePart, len(parts))
	for i, part := range parts {
		if part == nil {
			continue
		}
		partClone := *part
		partClone.InlineData = cloneFunctionResponseBlob(part.InlineData)
		partClone.FileData = cloneFunctionResponseFileData(part.FileData)
		clone[i] = &partClone
	}
	return clone
}

func cloneFunctionResponseBlob(v *genai.FunctionResponseBlob) *genai.FunctionResponseBlob {
	if v == nil {
		return nil
	}
	clone := *v
	clone.Data = cloneBytes(v.Data)
	return &clone
}

func cloneFunctionResponseFileData(v *genai.FunctionResponseFileData) *genai.FunctionResponseFileData {
	if v == nil {
		return nil
	}
	clone := *v
	return &clone
}

func cloneBytes(v []byte) []byte {
	if v == nil {
		return nil
	}
	clone := make([]byte, len(v))
	copy(clone, v)
	return clone
}
