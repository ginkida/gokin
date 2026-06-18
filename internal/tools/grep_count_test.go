package tools

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestGrep_ContextLinesDoNotInflateCount pins the fix: context lines are
// displayed (so the model sees surrounding code) but must NOT be counted as
// matches — otherwise grep -C2 misreports match density and misleads the model.
func TestGrep_ContextLinesDoNotInflateCount(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(f, []byte("a\nb\nMATCH\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt := NewGrepTool(dir)
	re := regexp.MustCompile("MATCH")

	// context_lines=2 → 1 match + 4 surrounding lines = 5 displayed entries.
	matches := gt.searchFile(f, re, 2)
	if got := countRealMatches(matches); got != 1 {
		t.Errorf("real match count = %d, want 1 (context lines must not inflate)", got)
	}
	if len(matches) != 5 {
		t.Errorf("displayed entries = %d, want 5 (match + 4 context)", len(matches))
	}

	// context_lines=0 → only the match, count unchanged.
	if got := countRealMatches(gt.searchFile(f, re, 0)); got != 1 {
		t.Errorf("no-context real count = %d, want 1", got)
	}

	// A context line that ALSO matches is counted as a match (not missed).
	f2 := filepath.Join(dir, "g.txt")
	if err := os.WriteFile(f2, []byte("MATCH\nx\nMATCH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// With context_lines=1 the two matches' windows overlap into one block of 3
	// lines, 2 of which are real matches.
	m2 := gt.searchFile(f2, re, 1)
	if got := countRealMatches(m2); got != 2 {
		t.Errorf("overlapping matches real count = %d, want 2", got)
	}
}
