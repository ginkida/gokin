package tools

import (
	"math"
	"sort"
	"strings"
)

// sortFileMatchesByRelevance ranks grep results in-place so the most
// likely-relevant file-hits appear first. Stable ordering is preserved
// for ties (path-asc tiebreaker) so re-running the same search yields
// the same top-N. Tie-breaker matters when the UI renders a capped list —
// deterministic output makes "where did that file go?" debuggable.
func sortFileMatchesByRelevance(results []fileMatch) {
	sort.SliceStable(results, func(i, j int) bool {
		si := fileRelevanceScore(results[i].path, len(results[i].matches))
		sj := fileRelevanceScore(results[j].path, len(results[j].matches))
		if si != sj {
			return si > sj
		}
		return results[i].path < results[j].path
	})
}

// fileRelevanceScore returns a relevance score for a path in a grep/glob
// result. Higher = better. Designed so a direct sort-desc by this value
// produces a reasonable candidate order for the model.
//
// Scoring factors (additive, but with log-shaped match contribution so
// a 100-hit vendor file can't drown a 5-hit source file):
//   - log2(matchCount+1) — diminishing returns on raw count: 1→1.0,
//     5→2.58, 10→3.46, 100→6.66. Preserves ordering within a tier but
//     caps the spread so other factors remain decisive across tiers.
//   - path-depth penalty: -0.3 per slash, capped at 6 levels
//   - non-test bonus (+1.2): test files rarely answer "where is X
//     implemented"; deprioritise but don't hide
//   - non-vendor bonus (+5.0): vendored / node_modules code almost
//     never deserves to outrank first-party source even on match count.
//     Large bonus because without it, a well-instrumented vendor file
//     with 100 hits can drown the 3 first-party hits the user actually
//     wanted.
//   - non-generated bonus (+0.7): *.pb.go / *_gen.go / *.min.js are
//     machine output.
//
// Pinned behaviour (TestSortFileMatchesByRelevance_Order): 2-hit source
// file outranks 12-hit vendor file outranks 8-hit test file.
func fileRelevanceScore(path string, matchCount int) float64 {
	if matchCount < 0 {
		matchCount = 0
	}
	// log2(n+1): 0→0, 1→1, 3→2, 7→3, 15→4 — monotonic but sub-linear.
	score := math.Log2(float64(matchCount + 1))

	// Depth penalty — count slashes, cap at 6 so a weirdly deep file
	// isn't penalised into oblivion (negative scores would flip the sort).
	depth := strings.Count(path, "/")
	if depth > 6 {
		depth = 6
	}
	score -= float64(depth) * 0.3

	if !isTestPath(path) {
		// Bonus sized so a 2-hit source file beats an 8-hit test file
		// (bias target: definition sites outrank usage sites for
		// "where is X?" — Kimi's dominant query pattern).
		score += 2.5
	}
	if !isVendorPath(path) {
		score += 5.0
	}
	if !isGeneratedPath(path) {
		score += 0.7
	}

	return score
}

// isTestPath flags paths that are tests, specs, or fixture directories.
// Deliberately conservative — _test.go / spec / fixtures are obvious;
// "test" as a bare directory name is also common enough to include.
func isTestPath(path string) bool {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, "_test.go") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".test.tsx") ||
		strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".test.jsx") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".spec.js") ||
		strings.HasSuffix(lower, "_spec.rb") {
		return true
	}
	// Python convention — test_foo.py or foo_test.py
	base := lower
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	if strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py") {
		return true
	}
	if strings.HasSuffix(base, "_test.py") {
		return true
	}
	// Directory signals
	segments := strings.Split(lower, "/")
	for _, seg := range segments {
		switch seg {
		case "__tests__", "tests", "test", "testdata", "fixtures", "__fixtures__":
			return true
		}
	}
	return false
}

// isVendorPath flags code we didn't author — vendored deps and build
// byproducts. Ranks these below first-party source even when they
// match more.
func isVendorPath(path string) bool {
	lower := strings.ToLower(path)
	markers := []string{
		"/vendor/", "/node_modules/", "/.git/",
		"/build/", "/dist/", "/target/",
		"/.venv/", "/venv/", "/__pycache__/",
		"/.next/", "/.nuxt/", "/.svelte-kit/",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	// Leading-segment variants (when the path is relative to repo root).
	prefixes := []string{
		"vendor/", "node_modules/", ".git/",
		"build/", "dist/", "target/",
		".venv/", "venv/", "__pycache__/",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// isGeneratedPath flags machine-produced files (proto, gen, minified).
// Grep on these yields thousands of hits that dominate the top of the
// list without ever being what the user was asking about.
func isGeneratedPath(path string) bool {
	lower := strings.ToLower(path)
	markers := []string{
		".pb.go", ".pb.ts", ".pb.py",
		"_gen.go", ".gen.go", ".gen.ts",
		"_generated.go", ".generated.ts",
		".min.js", ".min.css",
		".bundle.js",
	}
	for _, m := range markers {
		if strings.HasSuffix(lower, m) {
			return true
		}
	}
	return false
}
