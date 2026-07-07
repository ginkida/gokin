package commands

import "unicode/utf8"

// utf8SafeByteCut returns the largest n <= max such that s[:n] does not
// split a multi-byte UTF-8 rune. truncateDiffOutput/truncateGrepOutput/
// truncateShowOutput each cut at a fixed BYTE budget (not a rune count), then
// back up to the last newline when one exists within that window — but a
// single line at or beyond the byte budget with no newline (a minified
// bundle, a huge translation-table line) has no newline to back up to, and
// the raw byte offset can land mid-rune for non-ASCII content, producing
// invalid UTF-8 in the truncated output.
func utf8SafeByteCut(s string, max int) int {
	if max >= len(s) {
		return len(s)
	}
	if max <= 0 {
		return 0
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return max
}
