package ui

import "testing"

// TestTrailingInputToken_MidWordApostrophe pins the review fix for the compose
// tokenizer: an apostrophe inside a word (any English contraction) used to
// open quote mode and merge the whole rest of the line into one token — the
// @file autocomplete/ghost and plain path suggestions silently died for the
// remainder of the message ("don't forget @mai" → token "dont forget @mai",
// no @ prefix, no dropdown).
func TestTrailingInputToken_MidWordApostrophe(t *testing.T) {
	tok, ok := trailingInputToken("don't forget @mai")
	if !ok {
		t.Fatal("expected a trailing token")
	}
	if tok.text != "@mai" {
		t.Fatalf("trailing token = %q, want @mai (apostrophe must not merge tokens)", tok.text)
	}

	// atFileWord (the @file consumer) must see the @-ref after a contraction.
	if word, ok := atFileWord("let's look at @main"); !ok || word != "@main" {
		t.Fatalf("atFileWord after a contraction = (%q, %v), want (@main, true)", word, ok)
	}

	// Token-start quoting still works for paths with spaces.
	tok, ok = trailingInputToken(`open "my file`)
	if !ok || tok.text != "my file" {
		t.Fatalf("quoted trailing token = %q (%v), want 'my file'", tok.text, ok)
	}
}

// TestFormatPaletteArgEntryValue_QuotedPathWithSuffixNotDoubleQuoted pins the
// review fix: a user-quoted path followed by a line suffix ("my file.txt" 10)
// used to be wrapped in a SECOND quote layer, producing a filename with
// literal quote characters after the command parser decoded the outer layer.
// Quoting is also the escape hatch for filenames ending in a bare number, so
// it must round-trip.
func TestFormatPaletteArgEntryValue_QuotedPathWithSuffixNotDoubleQuoted(t *testing.T) {
	cmd := EnhancedPaletteCommand{ArgHint: "<file> [N|N-M|N M]"}
	got := formatPaletteArgEntryValue(cmd, `"my file.txt" 10`)
	if got != `"my file.txt" 10` {
		t.Fatalf("already-quoted path must round-trip, got %q", got)
	}
}
