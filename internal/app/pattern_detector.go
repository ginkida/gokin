package app

import (
	"fmt"
	"strings"
)

// toolPattern represents a detected repeating tool usage sequence.
type toolPattern struct {
	sequence []string // e.g., ["read", "grep", "edit"]
	count    int
}

// detectPatterns scans recentTools for repeating subsequences of length 2-4.
// Returns pattern descriptions like "You often follow read -> grep -> edit".
func (a *App) detectPatterns() []string {
	a.mu.Lock()
	recent := make([]string, len(a.recentTools))
	copy(recent, a.recentTools)
	a.mu.Unlock()

	if len(recent) < 4 {
		return nil
	}

	// Count subsequences of length 2-4
	counts := make(map[string]int)
	for seqLen := 2; seqLen <= 4; seqLen++ {
		for i := 0; i <= len(recent)-seqLen; i++ {
			key := strings.Join(recent[i:i+seqLen], "->")
			counts[key]++
		}
	}

	// Update toolPatterns and collect descriptions
	var patterns []toolPattern
	var descriptions []string
	for key, count := range counts {
		if count >= 3 {
			seq := strings.Split(key, "->")
			patterns = append(patterns, toolPattern{sequence: seq, count: count})
			descriptions = append(descriptions, fmt.Sprintf("You often follow %s (seen %d times)", strings.Join(seq, " -> "), count))
		}
	}

	a.mu.Lock()
	a.toolPatterns = patterns
	a.mu.Unlock()

	return descriptions
}

// getToolHints generates hints based on detected tool usage patterns.
// Returns hints only if patterns have count >= 3.
func (a *App) getToolHints() string {
	descriptions := a.detectPatterns()
	if len(descriptions) == 0 {
		return ""
	}

	var hints []string
	hints = append(hints, "Based on tool usage patterns observed in this session:")
	for _, desc := range descriptions {
		hints = append(hints, "- "+desc)
	}

	// Add general hints based on specific patterns
	a.mu.Lock()
	for _, p := range a.toolPatterns {
		if len(p.sequence) >= 2 && p.sequence[0] == "read" && p.count >= 3 {
			hints = append(hints, "- Consider using parallel reads for multiple files")
			break
		}
	}
	a.mu.Unlock()

	return strings.Join(hints, "\n")
}

// recordToolUsage appends a tool name to recentTools, keeping only the last 20.
func (a *App) recordToolUsage(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recentTools = append(a.recentTools, name)
	if len(a.recentTools) > 20 {
		a.recentTools = a.recentTools[len(a.recentTools)-20:]
	}
}
