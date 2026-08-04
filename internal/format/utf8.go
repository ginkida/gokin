package format

import "unicode/utf8"

// TrimSplitTrailingRune removes ONLY a trailing UTF-8 sequence that a byte-budget
// cut has split in half. It touches at most utf8.UTFMax-1 bytes and never scans
// the whole slice.
//
// The obvious-looking alternative — `for len(b) > 0 && !utf8.Valid(b) { b = b[:len(b)-1] }`
// — is a trap on two counts, and both bit real output paths:
//
//   - utf8.Valid inspects the ENTIRE buffer, so one invalid byte anywhere (any
//     command that emits binary or Latin-1) makes the condition true no matter
//     how much tail is removed. The loop then trims all the way back to that
//     byte, discarding everything after it — up to the whole transcript.
//   - Each iteration rescans from the start, so trimming n bytes costs O(n²).
//     At a megabyte buffer, under the writer's mutex, that is minutes of stall
//     in a streaming path.
//
// Bytes that were already invalid before the cut are left alone: they are the
// producer's data, not damage this function caused.
func TrimSplitTrailingRune(b []byte) []byte {
	// Walk back over continuation bytes to the start of the final sequence.
	// A rune is at most utf8.UTFMax bytes, so the search is bounded.
	for back := 1; back <= utf8.UTFMax && back <= len(b); back++ {
		index := len(b) - back
		if !utf8.RuneStart(b[index]) {
			continue // A continuation byte: the sequence starts further back.
		}
		rune_, size := utf8.DecodeRune(b[index:])
		if rune_ == utf8.RuneError && size <= 1 {
			return b[:index] // Incomplete or invalid at the tail: drop it.
		}
		if size == back {
			return b // The final sequence is complete.
		}
		return b[:index]
	}
	return b
}
