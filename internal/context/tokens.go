package context

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gokin/internal/client"
	"gokin/internal/config"

	"google.golang.org/genai"
)

// DefaultModelLimits provides default token limits for known models.
// Keys are used both for exact match and as substrings for fuzzy matching.
var DefaultModelLimits = map[string]TokenLimits{
	// Gemini family removed in v0.65 (provider gone) — six dead entries
	// removed in v0.78.30. Substring fuzzy match in lookupLimits never
	// matched anything since no Gemini model name reaches this code.
	// GLM — explicit entries avoid relying on substring-fuzzy fallback
	// (which would either miss 4.x variants entirely or return the generic
	// 128K/8K default from getModelLimits). glm-5.2 ships a 1M input
	// context window (Z.AI, Jun 2026); glm-5.1 and earlier 5.x stay at
	// 200K. Output caps are 128K across the 5.x line; older GLM families
	// differ per model.
	"glm-5.2": {
		MaxInputTokens:  1000000,
		MaxOutputTokens: 131072,
	},
	"glm-5.1": {
		MaxInputTokens:  200000,
		MaxOutputTokens: 131072,
	},
	"glm-5-turbo": {
		MaxInputTokens:  200000,
		MaxOutputTokens: 131072,
	},
	"glm-5": {
		MaxInputTokens:  200000,
		MaxOutputTokens: 131072,
	},
	"glm-4.7": {
		MaxInputTokens:  128000,
		MaxOutputTokens: 131072,
	},
	"glm-4.6": {
		MaxInputTokens:  128000,
		MaxOutputTokens: 131072,
	},
	"glm-4.5-air": {
		MaxInputTokens:  128000,
		MaxOutputTokens: 32768,
	},
	"glm-4.5": {
		MaxInputTokens:  128000,
		MaxOutputTokens: 131072,
	},
	"glm-4": {
		MaxInputTokens:  128000,
		MaxOutputTokens: 32768,
	},
	// MiniMax
	"minimax": {
		MaxInputTokens:  204800,
		MaxOutputTokens: 16384,
	},
	// Kimi — Coding Plan, 262K window
	"kimi": {
		MaxInputTokens:  262144,
		MaxOutputTokens: 32768,
	},
	// DeepSeek V4 — 1M input context, up to 384K output. Legacy
	// deepseek-chat / deepseek-reasoner currently route to V4 Flash
	// until their 2026-07-24 retirement.
	"deepseek": {
		MaxInputTokens:  1000000,
		MaxOutputTokens: 384000,
	},
}

// ModelPricing defines the cost per 1M tokens in USD.
type ModelPricing struct {
	InputCostPer1M       float64
	CachedInputCostPer1M float64 // 0 means cached input is billed at the normal input rate
	OutputCostPer1M      float64
}

// DefaultPricing provides cost estimation for known models.
var DefaultPricing = map[string]ModelPricing{
	// Gemini family removed in v0.65 — ten dead pricing entries deleted
	// in v0.78.30. CalculateCost would never look these up since model
	// names don't reach this map for removed providers.

	// GLM (prices in USD equivalent from CNY)
	// Z.AI bills cache hits at roughly one fifth of normal input. Keep the
	// cached rates explicit so /cost reflects provider-reported cache reads.
	"glm-5.2":     {InputCostPer1M: 4.00, CachedInputCostPer1M: 0.80, OutputCostPer1M: 16.00},
	"glm-5.1":     {InputCostPer1M: 4.00, CachedInputCostPer1M: 0.80, OutputCostPer1M: 16.00},
	"glm-5":       {InputCostPer1M: 1.00, CachedInputCostPer1M: 0.20, OutputCostPer1M: 4.00},
	"glm-5-turbo": {InputCostPer1M: 0.70, CachedInputCostPer1M: 0.14, OutputCostPer1M: 2.80},
	"glm-4.7":     {InputCostPer1M: 1.00, CachedInputCostPer1M: 0.20, OutputCostPer1M: 1.00},
	"glm-4.6":     {InputCostPer1M: 0.70, CachedInputCostPer1M: 0.14, OutputCostPer1M: 0.70},
	"glm-4.5":     {InputCostPer1M: 0.50, CachedInputCostPer1M: 0.10, OutputCostPer1M: 0.50},
	"glm-4.5-air": {InputCostPer1M: 0.14, CachedInputCostPer1M: 0.028, OutputCostPer1M: 0.14},
	"glm-4":       {InputCostPer1M: 1.00, OutputCostPer1M: 1.00},

	// MiniMax Pay-as-you-go pricing (USD / 1M tokens).
	"MiniMax-M2.7":           {InputCostPer1M: 0.30, OutputCostPer1M: 1.20},
	"MiniMax-M2.7-highspeed": {InputCostPer1M: 0.60, OutputCostPer1M: 2.40},
	"MiniMax-M2.5":           {InputCostPer1M: 0.30, OutputCostPer1M: 1.20},
	"MiniMax-M2.5-highspeed": {InputCostPer1M: 0.60, OutputCostPer1M: 2.40},
	"minimax":                {InputCostPer1M: 0.30, OutputCostPer1M: 1.20}, // fallback

	// Kimi K2.6 public API pricing (USD / 1M tokens). Coding Plan users
	// may be subscription-billed, but this keeps /cost close for paygo
	// and custom Moonshot endpoint usage.
	"kimi-for-coding": {InputCostPer1M: 0.95, OutputCostPer1M: 4.00},
	"kimi-k2.6":       {InputCostPer1M: 0.95, OutputCostPer1M: 4.00},
	"kimi":            {InputCostPer1M: 0.95, OutputCostPer1M: 4.00}, // fallback

	// DeepSeek V4 — public pricing as of May 2026 (USD / 1M tokens).
	// Legacy chat/reasoner retained until 2026-07-24.
	"deepseek-v4-flash": {InputCostPer1M: 0.14, OutputCostPer1M: 0.28},
	"deepseek-v4-pro":   {InputCostPer1M: 0.435, OutputCostPer1M: 0.87},
	"deepseek-chat":     {InputCostPer1M: 0.14, OutputCostPer1M: 0.28},
	"deepseek-reasoner": {InputCostPer1M: 0.14, OutputCostPer1M: 0.28},
	"deepseek":          {InputCostPer1M: 0.14, OutputCostPer1M: 0.28}, // fallback
}

// TokenLimits defines token limits for a model.
type TokenLimits struct {
	MaxInputTokens   int
	MaxOutputTokens  int
	WarningThreshold float64 // 0.8 = 80%
}

// TokenUsage represents current token usage statistics.
type TokenUsage struct {
	InputTokens  int
	MaxTokens    int
	PercentUsed  float64
	NearLimit    bool
	ExceedsLimit bool
	IsEstimate   bool // True when the provider or fallback used local estimation
}

// cacheEntry holds a cached token count with its key.
type cacheEntry struct {
	key        string
	tokens     int
	isEstimate bool
}

// TokenCounter handles token counting for context management.
type TokenCounter struct {
	client                   client.Client
	model                    string
	provider                 string
	limits                   TokenLimits
	maxInputTokensOverride   int
	warningThresholdOverride float64
	mu                       sync.RWMutex
	cache                    map[string]*list.Element // content hash -> list element
	lruList                  *list.List               // LRU list (front = most recent)
	maxCache                 int
}

// NewTokenCounter creates a new token counter.
func NewTokenCounter(c client.Client, model string, cfg *config.ContextConfig) *TokenCounter {
	limits := getModelLimits(model)

	// Apply config overrides
	if cfg != nil {
		if cfg.MaxInputTokens > 0 {
			limits.MaxInputTokens = cfg.MaxInputTokens
		}
		if cfg.WarningThreshold > 0 {
			limits.WarningThreshold = cfg.WarningThreshold
		}
	}

	// Default warning threshold if not set
	if limits.WarningThreshold == 0 {
		limits.WarningThreshold = 0.8
	}

	counter := &TokenCounter{
		client:   c,
		model:    model,
		limits:   limits,
		cache:    make(map[string]*list.Element),
		lruList:  list.New(),
		maxCache: 1000,
	}
	if identified, ok := c.(client.ProviderIdentity); ok {
		counter.provider = strings.ToLower(strings.TrimSpace(identified.GetProvider()))
	}
	if cfg != nil {
		counter.maxInputTokensOverride = cfg.MaxInputTokens
		counter.warningThresholdOverride = cfg.WarningThreshold
	}
	return counter
}

// SetClient updates the underlying client.
func (t *TokenCounter) SetClient(c client.Client) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.client = c
	t.provider = ""
	if identified, ok := c.(client.ProviderIdentity); ok {
		t.provider = strings.ToLower(strings.TrimSpace(identified.GetProvider()))
	}
	// Also update model limits as model might have changed
	t.model = ""
	if c != nil {
		t.model = c.GetModel()
	}
	t.limits = getModelLimits(t.model)
	t.applyOverridesLocked()
	t.cache = make(map[string]*list.Element)
	t.lruList.Init()
}

// SetConfig updates token-limit overrides without rebuilding the manager.
func (t *TokenCounter) SetConfig(cfg *config.ContextConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxInputTokensOverride = 0
	t.warningThresholdOverride = 0
	if cfg != nil {
		t.maxInputTokensOverride = cfg.MaxInputTokens
		t.warningThresholdOverride = cfg.WarningThreshold
	}
	t.limits = getModelLimits(t.model)
	t.applyOverridesLocked()
}

func (t *TokenCounter) applyOverridesLocked() {
	if t.maxInputTokensOverride > 0 {
		t.limits.MaxInputTokens = t.maxInputTokensOverride
	}
	if t.warningThresholdOverride > 0 {
		t.limits.WarningThreshold = t.warningThresholdOverride
	}
	if t.limits.WarningThreshold == 0 {
		t.limits.WarningThreshold = 0.8
	}
}

// GetModelLimits returns limits for a model, with fallback defaults.
func GetModelLimits(model string) TokenLimits {
	return getModelLimits(model)
}

// getModelLimits returns limits for a model, with fallback defaults.
// Uses exact match first, then fuzzy matching by checking if the model
// name contains a known base name (e.g. "glm-5.1-preview" matches "glm-5.1").
// Fuzzy match prefers the LONGEST matching key — "glm-4.5-preview" must map
// to "glm-4.5" (131K out), not the shorter "glm-4" (32K out). Go map iteration
// is unordered, so we sort keys by length descending to make the result
// deterministic.
func getModelLimits(model string) TokenLimits {
	// Exact match first
	if limits, ok := DefaultModelLimits[model]; ok {
		return limits
	}
	// Fuzzy match: check if model name contains a known base name.
	// Iterate keys longest-first so the most specific entry wins.
	modelLower := strings.ToLower(model)
	keys := make([]string, 0, len(DefaultModelLimits))
	for k := range DefaultModelLimits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, key := range keys {
		// Lowercase the key too — defensive consistency with getPricing, whose
		// sibling map (DefaultPricing) already has mixed-case keys. Today every
		// DefaultModelLimits key is lowercase so this is a no-op, but a future
		// mixed-case key would silently fall through to the default otherwise.
		if strings.Contains(modelLower, strings.ToLower(key)) {
			return DefaultModelLimits[key]
		}
	}
	// Default limits for unknown models
	return TokenLimits{
		MaxInputTokens:   128000,
		MaxOutputTokens:  8192,
		WarningThreshold: 0.8,
	}
}

// CountContents counts tokens for a list of contents using the API.
func (t *TokenCounter) CountContents(ctx context.Context, contents []*genai.Content) (int, error) {
	count, _, err := t.CountContentsWithAccuracy(ctx, contents)
	return count, err
}

// CountContentsWithAccuracy returns the token count and whether it is an
// estimate. Accuracy is cached with the count so a native-count fallback is
// never later relabelled as exact on a cache hit.
func (t *TokenCounter) CountContentsWithAccuracy(ctx context.Context, contents []*genai.Content) (int, bool, error) {
	// Try cache first
	hash := t.hashContents(contents)
	if entry, ok := t.getEntryFromCache(hash); ok {
		return entry.tokens, entry.isEstimate, nil
	}

	// Count via API
	t.mu.RLock()
	c := t.client
	t.mu.RUnlock()
	if c == nil {
		return 0, true, fmt.Errorf("token counter client is nil")
	}
	var resp *genai.CountTokensResponse
	var isEstimate bool
	var err error
	if detailed, ok := c.(client.TokenCountWithAccuracy); ok {
		resp, isEstimate, err = detailed.CountTokensWithAccuracy(ctx, contents)
	} else {
		resp, err = c.CountTokens(ctx, contents)
		if accuracy, ok := c.(client.TokenCountAccuracy); ok {
			isEstimate = accuracy.TokenCountIsEstimate()
		}
	}
	if err != nil {
		return 0, true, err
	}
	if resp == nil {
		return 0, true, fmt.Errorf("token counter client returned nil response")
	}

	count := int(resp.TotalTokens)

	// Cache the result
	t.addToCacheWithAccuracy(hash, count, isEstimate)

	return count, isEstimate, nil
}

// getFromCache retrieves a value from cache and moves it to front (LRU).
func (t *TokenCounter) getFromCache(key string) (int, bool) {
	entry, ok := t.getEntryFromCache(key)
	if !ok {
		return 0, false
	}
	return entry.tokens, true
}

func (t *TokenCounter) getEntryFromCache(key string) (cacheEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if elem, ok := t.cache[key]; ok {
		// Move to front (most recently used)
		t.lruList.MoveToFront(elem)
		return *elem.Value.(*cacheEntry), true
	}
	return cacheEntry{}, false
}

// addToCache adds a value to cache with LRU eviction.
func (t *TokenCounter) addToCache(key string, tokens int) {
	t.addToCacheWithAccuracy(key, tokens, false)
}

func (t *TokenCounter) addToCacheWithAccuracy(key string, tokens int, isEstimate bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if already in cache
	if elem, ok := t.cache[key]; ok {
		t.lruList.MoveToFront(elem)
		elem.Value.(*cacheEntry).tokens = tokens
		elem.Value.(*cacheEntry).isEstimate = isEstimate
		return
	}

	// Evict oldest if at capacity
	if t.lruList.Len() >= t.maxCache {
		oldest := t.lruList.Back()
		if oldest != nil {
			delete(t.cache, oldest.Value.(*cacheEntry).key)
			t.lruList.Remove(oldest)
		}
	}

	// Add new entry
	entry := &cacheEntry{key: key, tokens: tokens, isEstimate: isEstimate}
	elem := t.lruList.PushFront(entry)
	t.cache[key] = elem
}

// GetUsage returns current token usage statistics.
func (t *TokenCounter) GetUsage(tokenCount int) TokenUsage {
	// Snapshot limits under the lock: SetClient/SetConfig rewrite t.limits under
	// t.mu (a provider switch on a background worker), and this reader runs from
	// the onSessionChange auto-compaction path holding neither m.mu nor t.mu — a
	// genuine cross-goroutine race on a multi-word struct without the snapshot.
	t.mu.RLock()
	limits := t.limits
	t.mu.RUnlock()

	// Guard against a zero/unset limit: float division by zero yields +Inf,
	// which would make PercentUsed/NearLimit report "near limit" for any
	// token count. getModelLimits always returns a non-zero default, but a
	// zero-value TokenCounter or a bad config must not corrupt the readout.
	if limits.MaxInputTokens <= 0 {
		return TokenUsage{InputTokens: tokenCount, MaxTokens: 0}
	}
	percentUsed := float64(tokenCount) / float64(limits.MaxInputTokens)

	return TokenUsage{
		InputTokens:  tokenCount,
		MaxTokens:    limits.MaxInputTokens,
		PercentUsed:  percentUsed,
		NearLimit:    percentUsed >= limits.WarningThreshold,
		ExceedsLimit: tokenCount >= limits.MaxInputTokens,
	}
}

// CalculateCost estimates the USD cost for the given token usage.
func (t *TokenCounter) CalculateCost(inputTokens, outputTokens int) float64 {
	return t.CalculateCostWithCache(inputTokens, outputTokens, 0)
}

// CalculateCostWithCache estimates cost while pricing the provider-reported
// cache-read subset separately. inputTokens is the full prompt usage including
// cache hits; cacheReadTokens is clamped to that total defensively.
func (t *TokenCounter) CalculateCostWithCache(inputTokens, outputTokens, cacheReadTokens int) float64 {
	t.mu.RLock()
	model := t.model
	provider := t.provider
	t.mu.RUnlock()
	if provider == "ollama" {
		return 0
	}
	pricing := getPricing(model)
	cacheReadTokens = min(max(cacheReadTokens, 0), max(inputTokens, 0))
	uncachedInputTokens := max(inputTokens-cacheReadTokens, 0)
	cachedRate := pricing.CachedInputCostPer1M
	if cachedRate <= 0 {
		cachedRate = pricing.InputCostPer1M
	}
	inputCost := (float64(uncachedInputTokens) / 1000000.0) * pricing.InputCostPer1M
	inputCost += (float64(cacheReadTokens) / 1000000.0) * cachedRate
	outputCost := (float64(outputTokens) / 1000000.0) * pricing.OutputCostPer1M
	return inputCost + outputCost
}

// getPricing returns pricing for a model, with fallback defaults.
// Exact match wins; then longest-key substring match (so "deepseek-v4-pro"
// beats the "deepseek" fallback). All comparisons are case-insensitive so
// "MiniMax-M2.7" keys match the lowercased model name from the API.
func getPricing(model string) ModelPricing {
	modelLower := strings.ToLower(model)

	// Exact match (case-insensitive).
	for key, pricing := range DefaultPricing {
		if strings.EqualFold(key, modelLower) {
			return pricing
		}
	}

	// Longest-key substring match — prevents short fallback keys (e.g.
	// "deepseek", "glm-5") from winning over more specific ones
	// ("deepseek-v4-pro", "glm-5.1") due to random map iteration order.
	var bestKey string
	var bestPricing ModelPricing
	for key, pricing := range DefaultPricing {
		keyLower := strings.ToLower(key)
		if strings.Contains(modelLower, keyLower) && len(key) > len(bestKey) {
			bestKey = key
			bestPricing = pricing
		}
	}
	if bestKey != "" {
		return bestPricing
	}

	// Default to flash-tier pricing for unknown models. Pre-v0.78.30
	// this returned the deleted "gemini-1.5-flash" entry — zero
	// pricing — which silently understated cost.
	if p, ok := DefaultPricing["deepseek-v4-flash"]; ok {
		return p
	}
	return ModelPricing{}
}

// FormatCost returns a human-readable string for USD cost.
func FormatCost(cost float64) string {
	if cost == 0 {
		return "$0.00"
	}
	if cost < 0.0001 {
		return "< $0.0001"
	}
	return fmt.Sprintf("$%.4f", cost)
}

// GetLimits returns the current token limits (a value copy taken under the lock;
// SetClient/SetConfig can rewrite t.limits from another goroutine).
func (t *TokenCounter) GetLimits() TokenLimits {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.limits
}

// InvalidateCache clears all cached token counts.
// Should be called when history changes to force recalculation.
func (t *TokenCounter) InvalidateCache() {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Clear all cache entries
	t.cache = make(map[string]*list.Element)
	t.lruList.Init()
}

// hashContents creates a hash of contents for caching.
func (t *TokenCounter) hashContents(contents []*genai.Content) string {
	h := sha256.New()
	t.mu.RLock()
	c := t.client
	t.mu.RUnlock()
	if keyer, ok := c.(client.TokenCountCacheKey); ok {
		h.Write([]byte(keyer.TokenCountCacheKey()))
		h.Write([]byte{0})
	} else if c != nil {
		h.Write([]byte(c.GetModel()))
		h.Write([]byte{0})
	}
	for _, content := range contents {
		h.Write([]byte(content.Role))
		for _, part := range content.Parts {
			if part.Text != "" {
				h.Write([]byte(part.Text))
			}
			if part.FunctionCall != nil {
				h.Write([]byte(part.FunctionCall.Name))
				if argsJSON, err := json.Marshal(part.FunctionCall.Args); err == nil {
					h.Write(argsJSON)
				}
			}
			if part.FunctionResponse != nil {
				h.Write([]byte(part.FunctionResponse.Name))
				if respJSON, err := json.Marshal(part.FunctionResponse.Response); err == nil {
					h.Write(respJSON)
				}
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ContentType represents the type of content for token estimation.
type ContentType int

const (
	ContentTypeProse ContentType = iota
	ContentTypeCode
	ContentTypeJSON
	ContentTypeMixed
)

// EstimateTokens provides a rough estimate without API call.
// Uses a weighted combination of word-based and character-based estimation
// for better accuracy across different content types (prose, code, mixed).
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}

	// Detect content type and use appropriate estimation
	contentType := detectContentType(text)
	return estimateTokensForType(text, contentType)
}

// estimateTokensForType estimates tokens based on detected content type.
func estimateTokensForType(text string, contentType ContentType) int {
	// Count RUNES, not bytes: byte length over-inflates Cyrillic ~2× / CJK ~3×,
	// which crosses ExceedsLimit at ~60-65% real usage and fires premature
	// EmergencyTruncate. Mirrors the v0.85 rune-safety fix in
	// client/anthropic.go estimateTokens (this parallel copy was missed). ASCII
	// is byte-identical, so existing calibration is unaffected.
	chars := utf8.RuneCountInString(text)

	switch contentType {
	case ContentTypeCode:
		// Code has more tokens per character due to:
		// - camelCase and snake_case identifiers (split into multiple tokens)
		// - Short keywords (func, if, for, etc.)
		// - Operators and punctuation
		// Average: ~0.25 chars/token = 4 tokens/char... wait, that's inverted
		// Actually: ~3.2 chars per token for code
		return int(float64(chars) / 3.2)

	case ContentTypeJSON:
		// JSON is even more token-dense due to:
		// - Quotes, colons, commas, braces
		// - Keys are often tokenized separately
		// Average: ~3 chars per token
		return int(float64(chars) / 3.0)

	case ContentTypeProse:
		// Natural language text
		// Word-based estimation is better for prose
		words := len(strings.Fields(text))
		byWords := int(float64(words) * 1.3)

		// Character-based as fallback
		byChars := chars / 4

		// Weighted average favoring word-based
		return (byWords*3 + byChars) / 4

	default: // ContentTypeMixed
		// Use combined heuristic
		words := len(strings.Fields(text))
		byWords := int(float64(words) * 1.3)
		byChars := int(float64(chars) / 3.5) // Between code and prose

		return (byWords + byChars) / 2
	}
}

// detectContentType analyzes text to determine its type.
func detectContentType(text string) ContentType {
	if len(text) == 0 {
		return ContentTypeProse
	}

	// Quick heuristics based on content
	trimmed := strings.TrimSpace(text)

	// JSON detection
	if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
		return ContentTypeJSON
	}

	// Count code indicators
	codeIndicators := 0
	lines := strings.Split(text, "\n")
	totalLines := len(lines)

	for _, line := range lines {
		trimLine := strings.TrimSpace(line)

		// Go/C-style code indicators
		if strings.HasPrefix(trimLine, "func ") ||
			strings.HasPrefix(trimLine, "type ") ||
			strings.HasPrefix(trimLine, "package ") ||
			strings.HasPrefix(trimLine, "import ") ||
			strings.HasPrefix(trimLine, "//") ||
			strings.HasPrefix(trimLine, "/*") ||
			strings.HasPrefix(trimLine, "*/") ||
			strings.HasSuffix(trimLine, "{") ||
			strings.HasSuffix(trimLine, "}") ||
			strings.HasSuffix(trimLine, ";") ||
			strings.Contains(trimLine, " := ") ||
			strings.Contains(trimLine, "if err != nil") ||
			strings.Contains(trimLine, "return ") {
			codeIndicators++
		}

		// Python/JS/Other code indicators
		if strings.HasPrefix(trimLine, "def ") ||
			strings.HasPrefix(trimLine, "class ") ||
			strings.HasPrefix(trimLine, "function ") ||
			strings.HasPrefix(trimLine, "const ") ||
			strings.HasPrefix(trimLine, "let ") ||
			strings.HasPrefix(trimLine, "var ") ||
			strings.HasPrefix(trimLine, "#") ||
			strings.Contains(trimLine, " = ") {
			codeIndicators++
		}
	}

	// If more than 30% of lines look like code, treat as code
	if totalLines > 0 && float64(codeIndicators)/float64(totalLines) > 0.3 {
		return ContentTypeCode
	}

	// Check for camelCase/snake_case density (indicates code even without structure)
	camelCaseCount := 0
	words := strings.Fields(text)
	for _, word := range words {
		if containsCamelCase(word) || strings.Contains(word, "_") {
			camelCaseCount++
		}
	}

	if len(words) > 0 && float64(camelCaseCount)/float64(len(words)) > 0.2 {
		return ContentTypeMixed
	}

	return ContentTypeProse
}

// containsCamelCase checks if a word contains camelCase pattern.
func containsCamelCase(word string) bool {
	hasLower := false
	hasUpper := false
	for _, r := range word {
		if r >= 'a' && r <= 'z' {
			hasLower = true
		}
		if r >= 'A' && r <= 'Z' {
			hasUpper = true
		}
		// Transition from lower to upper indicates camelCase
		if hasLower && hasUpper {
			return true
		}
	}
	return false
}

// EstimateTokensWithType estimates tokens with explicit content type.
func EstimateTokensWithType(text string, contentType ContentType) int {
	if text == "" {
		return 0
	}
	return estimateTokensForType(text, contentType)
}

// EstimateContentsTokens estimates tokens for contents without API call.
func EstimateContentsTokens(contents []*genai.Content) int {
	total := 0
	for _, content := range contents {
		// Role overhead
		total += 4
		for _, part := range content.Parts {
			if part.Text != "" {
				total += EstimateTokens(part.Text)
			}
			if part.FunctionCall != nil {
				total += 10 + EstimateTokens(part.FunctionCall.Name)
				// Estimate args
				for k, v := range part.FunctionCall.Args {
					total += EstimateTokens(k)
					if str, ok := v.(string); ok {
						total += EstimateTokens(str)
					} else {
						total += 10 // estimate for non-string args
					}
				}
			}
			if part.FunctionResponse != nil {
				total += 10 + EstimateTokens(part.FunctionResponse.Name)
				// Estimate response map - Response is already map[string]any
				for k, v := range part.FunctionResponse.Response {
					total += EstimateTokens(k)
					if str, ok := v.(string); ok {
						total += EstimateTokens(str)
					} else {
						total += 10
					}
				}
			}
		}
	}
	return total
}
