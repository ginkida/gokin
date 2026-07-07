package commands

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestUtf8SafeByteCut_NeverSplitsARune (round 6) pins the fix for
// truncateDiffOutput/truncateGrepOutput/truncateShowOutput: each cuts at a
// fixed BYTE budget (not a rune count), then backs up to the last newline
// when one exists within the window — but a single line at or beyond the
// byte budget with no newline (a minified bundle, a huge translation-table
// line) has nothing to back up to, and the raw byte offset can land mid-rune
// for non-ASCII content, producing invalid UTF-8.
func TestUtf8SafeByteCut_NeverSplitsARune(t *testing.T) {
	// A 3-byte rune ("日" = 0xE6 0x97 0xA5) whose bytes straddle every
	// possible cut position within its span.
	s := "abc日def"
	// s = 'a'(0) 'b'(1) 'c'(2) then 日's 3 bytes at indices 3,4,5, then 'd'(6)...
	for cut := 3; cut <= 5; cut++ {
		n := utf8SafeByteCut(s, cut)
		if !utf8.ValidString(s[:n]) {
			t.Errorf("utf8SafeByteCut(%q, %d) = %d, s[:%d]=%q is invalid UTF-8", s, cut, n, n, s[:n])
		}
	}

	// Boundary cases.
	if n := utf8SafeByteCut("hello", 100); n != 5 {
		t.Errorf("max >= len(s): got %d, want 5", n)
	}
	if n := utf8SafeByteCut("hello", 0); n != 0 {
		t.Errorf("max == 0: got %d, want 0", n)
	}
	if n := utf8SafeByteCut("hello", 3); n != 3 {
		t.Errorf("all-ASCII cut should be exact: got %d, want 3", n)
	}
}

// TestTruncateOutput_SingleLineWithMultiByteRuneAtBoundary is a table over
// all three truncateXOutput helpers, each fed a single line (no newline —
// the existing "back up to last newline" rescue can't help) with a 3-byte
// UTF-8 rune positioned so its bytes straddle the exact byte-budget cut
// point.
func TestTruncateOutput_SingleLineWithMultiByteRuneAtBoundary(t *testing.T) {
	build := func(maxBytes int) string {
		// Put the multi-byte rune's first byte exactly at index maxBytes-1,
		// so the naive s[:maxBytes] slice includes only its first byte.
		return strings.Repeat("x", maxBytes-1) + "日" + strings.Repeat("y", 200)
	}

	cases := []struct {
		name string
		fn   func(string) string
		max  int
	}{
		{"diff", truncateDiffOutput, diffMaxOutputBytes},
		{"grep", truncateGrepOutput, grepMaxOutputBytes},
		{"show", truncateShowOutput, showMaxOutputBytes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := build(tc.max)
			got := tc.fn(s)
			if !utf8.ValidString(got) {
				t.Fatalf("%s truncation produced invalid UTF-8 output", tc.name)
			}
		})
	}
}
