package format

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTrimSplitTrailingRuneOnlyRemovesASplitSequence(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"empty", []byte{}, []byte{}},
		{"ascii", []byte("hello"), []byte("hello")},
		{"complete multibyte", []byte("привет"), []byte("привет")},
		{"split two-byte", []byte("привет")[:len("привет")-1], []byte("приве")},
		{"split four-byte", []byte("a😀")[:2], []byte("a")},
		{"complete four-byte", []byte("a😀"), []byte("a😀")},
		// A byte that was ALREADY invalid before any cut is the producer's data,
		// not damage this function caused — it must survive.
		{"pre-existing invalid byte", []byte("ok\xffdata"), []byte("ok\xffdata")},
		{"trailing lone invalid byte", []byte("ok\xff"), []byte("ok")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TrimSplitTrailingRune(tc.in); !bytes.Equal(got, tc.want) {
				t.Fatalf("TrimSplitTrailingRune(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The loop this replaced validated the WHOLE buffer each iteration, so a single
// invalid byte made the condition permanently true: it trimmed everything back
// to that byte, discarding the rest of the transcript, at O(n²) cost under a
// writer's mutex. Both properties are pinned here.
func TestTrimSplitTrailingRuneIsBoundedAndKeepsDataAfterAnInvalidByte(t *testing.T) {
	payload := append([]byte("\xff"), bytes.Repeat([]byte("x"), 1<<20)...)

	started := time.Now()
	got := TrimSplitTrailingRune(payload)
	elapsed := time.Since(started)

	if len(got) != len(payload) {
		t.Fatalf("dropped %d bytes after a pre-existing invalid byte", len(payload)-len(got))
	}
	if elapsed > time.Second {
		t.Fatalf("took %v on a 1 MiB buffer — the scan is not bounded", elapsed)
	}
}

// Whatever it returns must be usable as text when the input was valid up to the
// cut, which is the entire point of calling it after a byte-budget truncation.
func TestTrimSplitTrailingRuneLeavesValidTextValid(t *testing.T) {
	text := strings.Repeat("日本語テキスト", 64)
	for cut := 1; cut < len(text); cut += 7 {
		got := TrimSplitTrailingRune([]byte(text[:cut]))
		if !utf8.Valid(got) {
			t.Fatalf("cut at %d produced invalid UTF-8: %q", cut, got)
		}
	}
}
