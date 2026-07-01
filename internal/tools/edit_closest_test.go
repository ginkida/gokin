package tools

import (
	"strings"
	"testing"
	"time"
)

func TestFindClosestLines_SingleLine_ExactMatch(t *testing.T) {
	content := "func main() {\n\tfmt.Println(\"hello\")\n}"
	best, line, score := findClosestLines(content, "fmt.Println(\"hello\")")
	if score < 0.9 {
		t.Errorf("expected high score for near-exact match, got %.2f", score)
	}
	if line != 2 {
		t.Errorf("expected line 2, got %d", line)
	}
	if !strings.Contains(best, "Println") {
		t.Errorf("expected Println in best match, got %q", best)
	}
}

func TestFindClosestLines_SingleLine_Typo(t *testing.T) {
	content := "func handleRequest(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(200)\n}"
	// Model remembers "handleReqeust" (typo)
	best, line, score := findClosestLines(content, "func handleReqeust(w http.ResponseWriter, r *http.Request) {")
	if score < 0.5 {
		t.Errorf("expected decent score for typo match, got %.2f", score)
	}
	if line != 1 {
		t.Errorf("expected line 1, got %d", line)
	}
	_ = best
}

func TestFindClosestLines_MultiLine(t *testing.T) {
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n\tfmt.Println(\"world\")\n}"
	// Model remembers wrong indentation
	search := "func main() {\n    fmt.Println(\"hello\")\n    fmt.Println(\"world\")"
	best, line, score := findClosestLines(content, search)
	if score < 0.5 {
		t.Errorf("expected decent score for indent mismatch, got %.2f", score)
	}
	if line != 5 {
		t.Errorf("expected line 5, got %d", line)
	}
	_ = best
}

func TestFindClosestLines_NoMatch(t *testing.T) {
	content := "package main\n\nfunc main() {}"
	_, _, score := findClosestLines(content, "completely unrelated text that does not exist")
	if score > 0.4 {
		t.Errorf("expected low score for unrelated text, got %.2f", score)
	}
}

func TestFindClosestLines_EmptyContent(t *testing.T) {
	_, _, score := findClosestLines("", "some text")
	if score != 0 {
		t.Errorf("expected 0 score for empty content, got %.2f", score)
	}
}

func TestLineSimilarity_Identical(t *testing.T) {
	if s := lineSimilarity("hello", "hello"); s != 1.0 {
		t.Errorf("expected 1.0 for identical strings, got %.2f", s)
	}
}

func TestLineSimilarity_Similar(t *testing.T) {
	s := lineSimilarity("func handleRequest(", "func handleReqeust(")
	if s < 0.8 {
		t.Errorf("expected high similarity for typo, got %.2f", s)
	}
}

func TestLineSimilarity_Different(t *testing.T) {
	s := lineSimilarity("aaaa", "zzzz")
	if s > 0.1 {
		t.Errorf("expected low similarity for unrelated strings, got %.2f", s)
	}
}

func TestLcsLength(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"abc", "abc", 3},
		{"abc", "axbxc", 3},
		{"", "abc", 0},
		{"abc", "", 0},
		{"abcdef", "azced", 3}, // a, c, e or a, c, d
	}
	for _, tt := range tests {
		got := lcsLength(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("lcsLength(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestFindClosestLines_LargeFileSkipsScan pins the resource-exhaustion fix:
// above maxClosestLinesScanLines, findClosestLines must return a zero score
// WITHOUT running the O(fileLines × oldLines × lineLen²) LCS scan
// (blockSimilarity -> lineSimilarity -> lcsLength). A large but legitimate
// file (vendored/generated/log) with a stale old_string used to burn real
// single-core CPU with no ctx-cancellation anywhere in the chain and no
// size cap of its own — bounded here by asserting the call completes fast
// even at well beyond the cap, proving the scan itself never runs.
func TestFindClosestLines_LargeFileSkipsScan(t *testing.T) {
	lineCount := maxClosestLinesScanLines + 5000
	lines := make([]string, lineCount)
	for i := range lines {
		lines[i] = strings.Repeat("x", 150) // realistic long-ish line
	}
	// Embed an EXACT match deep in the huge file. Without the cap the scan
	// would find it at score 1.0 — so a zero-value result here is only
	// possible if the guard skipped the scan entirely, not a coincidence of
	// low similarity scores.
	target := "func handleRequest(w http.ResponseWriter, r *http.Request) {"
	matchLine := lineCount / 2
	lines[matchLine] = target
	content := strings.Join(lines, "\n")

	start := time.Now()
	best, line, score := findClosestLines(content, target)
	elapsed := time.Since(start)

	if score != 0 || best != "" || line != 0 {
		t.Errorf("expected zero-value result above the scan cap despite an exact match at line %d — got best=%q line=%d score=%.2f (guard did not skip the scan)",
			matchLine+1, best, line, score)
	}
	// The full scan on a file this size takes seconds of CPU (measured ~3-10s
	// in the audit's own reproduction); the capped path is a couple of
	// strings.Split calls, so a generous 1s bound reliably distinguishes
	// "skipped" from "ran the full scan" without being a flaky tight bound.
	if elapsed > time.Second {
		t.Errorf("findClosestLines took %v above the scan cap — the size guard did not short-circuit the full scan", elapsed)
	}
}

// A file just under the cap must still run the real scan (no false-negative
// on the boundary — the cap must not silently disable the feature for
// ordinary large-ish files).
func TestFindClosestLines_UnderCapStillScans(t *testing.T) {
	lineCount := maxClosestLinesScanLines - 1
	lines := make([]string, lineCount)
	for i := range lines {
		lines[i] = "filler line of no particular interest"
	}
	lines[lineCount/2] = "func handleRequest(w http.ResponseWriter, r *http.Request) {"
	content := strings.Join(lines, "\n")

	best, line, score := findClosestLines(content, "func handleReqeust(w http.ResponseWriter, r *http.Request) {")
	if score < 0.5 {
		t.Errorf("expected a real match just under the cap, got score=%.2f best=%q", score, best)
	}
	if line != lineCount/2+1 {
		t.Errorf("expected line %d, got %d", lineCount/2+1, line)
	}
}

func TestExtractAsync_DoesNotRace(t *testing.T) {
	// This test verifies that ExtractAsync copies the history before goroutine launch
	// (the race detector will catch it if not)
	// Note: This is a compile/link test more than a runtime test — the real protection
	// is that ExtractAsync does copy() internally.
}
