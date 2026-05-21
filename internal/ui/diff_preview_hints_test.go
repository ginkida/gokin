package ui

import (
	"os"
	"strings"
	"testing"
)

// TestDiffPreviewHints_UseConsistentSeparator pins that the diff preview's
// key-hint footers use `·` (middle-dot, double-space padded) separators,
// matching the rest of the app (welcome panel tips, prompt option lines,
// shortcuts overlay). Previously diff_preview used `|` with single spaces
// — heavier and inconsistent with everything else.
//
// Rendering the View requires a complex Model fixture (private fields,
// helper-only construction). Easier: scan the source file for the legacy
// `|` separator embedded in hint strings. If a regression reintroduces
// `Foo: Bar | Baz: Qux` shape in a footer, this trips.
func TestDiffPreviewHints_UseConsistentSeparator(t *testing.T) {
	raw, err := os.ReadFile("diff_preview.go")
	if err != nil {
		t.Fatalf("read diff_preview.go: %v", err)
	}
	src := string(raw)

	// Legacy patterns from the pre-polish footers. If any of these
	// substrings reappear in a hint literal, we've regressed.
	legacy := []string{
		"j/k: Scroll | g/G",                  // single-line hint
		"s: Toggle split/unified | A: Accept", // single-line hint #2
		"Tab: Switch focus | ↑/↓",             // multi-file hint
	}
	for _, snippet := range legacy {
		if strings.Contains(src, snippet) {
			t.Errorf("diff_preview.go still contains legacy `|` separator hint: %q", snippet)
		}
	}

	// Sanity: at least one polished `·`-separated hint line exists. Catches
	// "someone removed the hint entirely" regressions too.
	polished := []string{
		"j/k Scroll  ·  g/G Top/Bottom",
		"Tab Switch focus  ·  ↑/↓ Files",
	}
	for _, snippet := range polished {
		if !strings.Contains(src, snippet) {
			t.Errorf("diff_preview.go missing polished hint substring: %q", snippet)
		}
	}
}
