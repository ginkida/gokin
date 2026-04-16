package tools

import (
	"fmt"
	"sort"
	"strings"
)

// formatUnknownToolError produces an "unknown tool" error message that nudges
// weak models back to a valid tool name. It lists up to three fuzzy matches
// (edit distance ≤ 3 or shared prefix ≥ 3 chars) so the model can self-correct
// without another round trip. Kept short on purpose — long tool dumps waste
// context and make the model ignore the hint.
func formatUnknownToolError(requested string, available []string) string {
	suggestions := suggestToolNames(requested, available, 3)
	if len(suggestions) == 0 {
		return fmt.Sprintf("unknown tool: %s", requested)
	}
	return fmt.Sprintf("unknown tool: %s. Did you mean: %s?", requested, strings.Join(suggestions, ", "))
}

// suggestToolNames ranks candidate tool names by closeness to `requested`.
// Ranking prefers: (a) shared case-insensitive prefix of ≥ 3 chars, (b) lower
// Levenshtein distance. Candidates with distance > threshold are dropped.
func suggestToolNames(requested string, available []string, maxResults int) []string {
	if requested == "" || len(available) == 0 {
		return nil
	}
	threshold := 3
	if len(requested) <= 4 {
		threshold = 2 // shorter names need tighter matching to be useful
	}
	req := strings.ToLower(requested)

	type scored struct {
		name     string
		distance int
		prefix   int // shared prefix length (higher is better)
	}
	candidates := make([]scored, 0, len(available))
	for _, name := range available {
		lname := strings.ToLower(name)
		dist := levenshtein(req, lname)
		prefix := commonPrefixLen(req, lname)
		// Keep anything within distance threshold or sharing a meaningful prefix.
		if dist <= threshold || prefix >= 3 {
			candidates = append(candidates, scored{name: name, distance: dist, prefix: prefix})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return candidates[i].prefix > candidates[j].prefix
	})
	if len(candidates) > maxResults {
		candidates = candidates[:maxResults]
	}
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.name
	}
	return names
}

// levenshtein returns the edit distance between two strings using the classic
// dynamic-programming algorithm. Allocates a single row buffer.
func levenshtein(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	la, lb := len(ra), len(rb)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}
