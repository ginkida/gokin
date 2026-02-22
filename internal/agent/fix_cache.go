package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ErrorSignature captures the distinctive features of an error for fuzzy matching.
type ErrorSignature struct {
	Category  string   // "compilation_error", "file_not_found", etc.
	ToolName  string   // Tool that produced the error
	KeyTokens []string // Distinctive tokens extracted from error message
}

// Key returns a stable string key for map storage.
func (s ErrorSignature) Key() string {
	return s.Category + ":" + s.ToolName + ":" + strings.Join(s.KeyTokens, ",")
}

// FixCall records a single tool call that was part of a fix sequence.
type FixCall struct {
	ToolName string
	Args     map[string]any
	Success  bool
}

// FixRecord stores a cached error→fix mapping.
type FixRecord struct {
	Signature    ErrorSignature
	ErrorMsg     string    // Original error message for display
	FixCalls     []FixCall // Tool calls that constituted the fix
	HitCount     int       // Times cache was hit
	SuccessCount int       // Times cached fix led to recovery
	FailCount    int
	CreatedAt    time.Time
	LastUsedAt   time.Time
}

// PendingError tracks an error waiting for fix detection.
type PendingError struct {
	Signature ErrorSignature
	ErrorMsg  string
	TurnIndex int
	FixBuffer []FixCall // Accumulates successful calls after this error
}

// CategoryCounter tracks error frequency per category.
type CategoryCounter struct {
	Count     int
	ToolNames map[string]int
}

// FixCache is a session-scoped, thread-safe cache that remembers error→fix mappings
// discovered during the current agent run. It detects fixes by observing successful
// tool calls after a failure and provides instant lookup on recurrence.
type FixCache struct {
	mu               sync.RWMutex
	fixes            map[string]*FixRecord  // key → fix record
	pending          []*PendingError        // errors awaiting fix detection
	categoryCounters map[string]*CategoryCounter
	maxPending       int // max pending errors to track
	maxFixes         int // max fix records to keep
	fixWindow        int // successful calls needed to graduate a fix
}

// NewFixCache creates a new session-local fix cache.
func NewFixCache() *FixCache {
	return &FixCache{
		fixes:            make(map[string]*FixRecord),
		pending:          make([]*PendingError, 0, 10),
		categoryCounters: make(map[string]*CategoryCounter),
		maxPending:       10,
		maxFixes:         50,
		fixWindow:        3,
	}
}

// BuildSignature creates an ErrorSignature from error context.
func BuildSignature(toolName, category, errorMsg string) ErrorSignature {
	return ErrorSignature{
		Category:  category,
		ToolName:  toolName,
		KeyTokens: extractKeyTokens(errorMsg),
	}
}

// Lookup searches cached fixes for a fuzzy match against the given error.
// Returns the best matching FixRecord and true if found.
func (fc *FixCache) Lookup(toolName, category, errorMsg string) (*FixRecord, bool) {
	if category == "" {
		return nil, false
	}

	sig := BuildSignature(toolName, category, errorMsg)

	fc.mu.RLock()
	defer fc.mu.RUnlock()

	var bestMatch *FixRecord
	var bestScore float64

	for _, fix := range fc.fixes {
		if fix.Signature.Category != sig.Category {
			continue
		}
		score := jaccardSimilarity(sig.KeyTokens, fix.Signature.KeyTokens)
		if score >= 0.5 && score > bestScore {
			bestScore = score
			bestMatch = fix
		}
	}

	return bestMatch, bestMatch != nil
}

// RecordError pushes a new pending error for fix detection tracking.
func (fc *FixCache) RecordError(toolName string, args map[string]any, category, errorMsg string, turnIdx int) {
	if category == "" {
		return
	}

	sig := BuildSignature(toolName, category, errorMsg)

	fc.mu.Lock()
	defer fc.mu.Unlock()

	// If this error matches an existing fix, it means the cached fix didn't work — record failure
	for _, fix := range fc.fixes {
		if fix.Signature.Category == sig.Category {
			score := jaccardSimilarity(sig.KeyTokens, fix.Signature.KeyTokens)
			if score >= 0.5 {
				fix.FailCount++
				break
			}
		}
	}

	// Update category counter
	cc, ok := fc.categoryCounters[category]
	if !ok {
		cc = &CategoryCounter{ToolNames: make(map[string]int)}
		fc.categoryCounters[category] = cc
	}
	cc.Count++
	cc.ToolNames[toolName]++

	// Evict oldest pending if at capacity
	if len(fc.pending) >= fc.maxPending {
		fc.pending = fc.pending[1:]
	}

	fc.pending = append(fc.pending, &PendingError{
		Signature: sig,
		ErrorMsg:  errorMsg,
		TurnIndex: turnIdx,
		FixBuffer: make([]FixCall, 0, fc.fixWindow),
	})
}

// RecordSuccess feeds a successful tool call into fix detection.
// When enough successful calls follow a pending error, the sequence is graduated into a fix record.
func (fc *FixCache) RecordSuccess(toolName string, args map[string]any) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	call := FixCall{
		ToolName: toolName,
		Args:     cloneArgs(args),
		Success:  true,
	}

	// Feed into all pending errors' fix buffers
	graduated := make([]int, 0)
	for i, pe := range fc.pending {
		pe.FixBuffer = append(pe.FixBuffer, call)
		if len(pe.FixBuffer) >= fc.fixWindow {
			// Graduate: this pending error now has a detected fix
			fc.graduateFix(pe)
			graduated = append(graduated, i)
		}
	}

	// Remove graduated entries (reverse order to preserve indices)
	for j := len(graduated) - 1; j >= 0; j-- {
		idx := graduated[j]
		fc.pending = append(fc.pending[:idx], fc.pending[idx+1:]...)
	}
}

// RecordHit increments the hit counter for a fix record without recording outcome.
// Outcome (success/fail) is determined later by whether the same error recurs.
func (fc *FixCache) RecordHit(key string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()

	fix, ok := fc.fixes[key]
	if !ok {
		return
	}
	fix.HitCount++
	fix.LastUsedAt = time.Now()
}

// GetAggregation returns meta-guidance if a category has been seen frequently (>=3 times).
func (fc *FixCache) GetAggregation(category string) string {
	if category == "" {
		return ""
	}

	fc.mu.RLock()
	defer fc.mu.RUnlock()

	cc, ok := fc.categoryCounters[category]
	if !ok || cc.Count < 3 {
		return ""
	}

	// Build tool frequency list
	var topTools []string
	for tn, cnt := range cc.ToolNames {
		topTools = append(topTools, fmt.Sprintf("`%s` (%d)", tn, cnt))
	}

	switch category {
	case "compilation_error":
		return fmt.Sprintf(
			"**Pattern alert:** %d compilation errors this session (tools: %s). Consider running `go build ./...` proactively before further edits.",
			cc.Count, strings.Join(topTools, ", "))
	case "file_not_found":
		return fmt.Sprintf(
			"**Pattern alert:** %d file-not-found errors this session (tools: %s). Consider using `glob` to verify paths before operations.",
			cc.Count, strings.Join(topTools, ", "))
	case "syntax_error":
		return fmt.Sprintf(
			"**Pattern alert:** %d syntax errors this session (tools: %s). Consider reading files before editing to verify current content.",
			cc.Count, strings.Join(topTools, ", "))
	default:
		return fmt.Sprintf(
			"**Pattern alert:** %d %s errors this session (tools: %s). Consider a different approach.",
			cc.Count, category, strings.Join(topTools, ", "))
	}
}

// graduateFix promotes a pending error + its fix buffer into a cached fix record.
// Must be called under fc.mu write lock.
func (fc *FixCache) graduateFix(pe *PendingError) {
	key := pe.Signature.Key()

	// If we already have a fix for this key, update it
	if existing, ok := fc.fixes[key]; ok {
		existing.FixCalls = pe.FixBuffer
		existing.LastUsedAt = time.Now()
		return
	}

	// Evict least-used fix if at capacity
	if len(fc.fixes) >= fc.maxFixes {
		fc.evictLeastUsed()
	}

	fc.fixes[key] = &FixRecord{
		Signature:  pe.Signature,
		ErrorMsg:   pe.ErrorMsg,
		FixCalls:   pe.FixBuffer,
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
	}
}

// evictLeastUsed removes the fix record with the lowest hit count.
// Must be called under fc.mu write lock.
func (fc *FixCache) evictLeastUsed() {
	var worstKey string
	worstScore := int(^uint(0) >> 1) // max int

	for k, fix := range fc.fixes {
		score := fix.HitCount + fix.SuccessCount
		if score < worstScore {
			worstScore = score
			worstKey = k
		}
	}

	if worstKey != "" {
		delete(fc.fixes, worstKey)
	}
}

// FormatCachedFix builds a human-readable intervention string from a cached fix record.
func FormatCachedFix(fix *FixRecord) string {
	var sb strings.Builder
	sb.WriteString("**Session Fix Cache Hit:**\n\n")
	sb.WriteString(fmt.Sprintf("A similar %s error was fixed earlier in this session.\n\n", fix.Signature.Category))
	sb.WriteString(fmt.Sprintf("**Original error:** %s\n\n", truncateStr(fix.ErrorMsg, 200)))
	sb.WriteString("**Fix sequence:**\n")
	for i, call := range fix.FixCalls {
		sb.WriteString(fmt.Sprintf("  %d. `%s`", i+1, call.ToolName))
		if len(call.Args) > 0 {
			// Show key args briefly
			var argParts []string
			for k, v := range call.Args {
				s := fmt.Sprintf("%v", v)
				if len(s) > 60 {
					s = s[:60] + "..."
				}
				argParts = append(argParts, fmt.Sprintf("%s=%s", k, s))
			}
			if len(argParts) > 3 {
				argParts = argParts[:3]
				argParts = append(argParts, "...")
			}
			sb.WriteString(fmt.Sprintf(" (%s)", strings.Join(argParts, ", ")))
		}
		sb.WriteString("\n")
	}
	if fix.HitCount > 0 {
		sb.WriteString(fmt.Sprintf("\n(Cache stats: %d hits, %d successes)\n", fix.HitCount, fix.SuccessCount))
	}
	sb.WriteString("\nApply a similar fix sequence to resolve this error.\n")
	return sb.String()
}

// --- Helper functions ---

// extractKeyTokens splits an error message into distinctive tokens for fuzzy matching.
// Removes common stop words and keeps paths, identifiers, and error-specific words.
func extractKeyTokens(errorMsg string) []string {
	// Normalize
	msg := strings.ToLower(errorMsg)

	// Split on non-alphanumeric (preserving path separators as tokens)
	var tokens []string
	var current strings.Builder
	for _, r := range msg {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '/' || r == '.' || r == '_' || r == '-' {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}

	// Filter out stop words and very short tokens
	filtered := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if len(t) <= 2 || stopWords[t] {
			continue
		}
		filtered = append(filtered, t)
	}

	// Cap at 20 tokens to keep signatures manageable
	if len(filtered) > 20 {
		filtered = filtered[:20]
	}
	return filtered
}

// jaccardSimilarity computes the Jaccard similarity coefficient between two token sets.
func jaccardSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}

	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}

	intersection := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersection++
		}
	}

	union := len(setA)
	for t := range setB {
		if _, ok := setA[t]; !ok {
			union++
		}
	}

	if union == 0 {
		return 0.0
	}
	return float64(intersection) / float64(union)
}

// cloneArgs creates a shallow copy of tool arguments.
func cloneArgs(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}
	c := make(map[string]any, len(args))
	for k, v := range args {
		c[k] = v
	}
	return c
}

// truncateStr shortens a string to maxLen characters, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// stopWords contains common English words that don't contribute to error signature matching.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "not": true, "but": true,
	"with": true, "this": true, "that": true, "from": true, "are": true,
	"was": true, "were": true, "been": true, "have": true, "has": true,
	"had": true, "does": true, "did": true, "will": true, "would": true,
	"could": true, "should": true, "may": true, "might": true, "can": true,
	"must": true, "shall": true, "need": true, "use": true, "used": true,
	"try": true, "also": true, "into": true, "than": true, "then": true,
	"when": true, "where": true, "which": true, "while": true, "who": true,
	"each": true, "every": true, "all": true, "any": true, "few": true,
	"more": true, "most": true, "some": true, "such": true, "only": true,
	"own": true, "same": true, "too": true, "very": true, "just": true,
	"error": true, "failed": true, "failure": true,
}
