package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortenPath_ASCII(t *testing.T) {
	cases := []struct {
		path   string
		maxLen int
		want   string
	}{
		{"/short.go", 20, "/short.go"},
		// availableLen = 20 - len("file.go") - 4 = 9; result = "/very/lon" + "..." + "file.go".
		{"/very/long/path/to/some/file.go", 20, "/very/lon...file.go"},
		// filename longer than maxLen → fallback "..." + filename.
		{"/a/b/c/very_long_filename.go", 10, "...very_long_filename.go"},
	}
	for _, tc := range cases {
		got := shortenPath(tc.path, tc.maxLen)
		if got != tc.want {
			t.Errorf("shortenPath(%q, %d) = %q, want %q", tc.path, tc.maxLen, got, tc.want)
		}
	}
}

// Regression test for v0.79.3: shortenPath was byte-slicing multibyte paths,
// producing invalid UTF-8 when the slice boundary fell inside a Cyrillic or
// CJK rune. Bug surfaced as "..." with mojibake in the Read/Write/Edit
// display names whenever the user worked in non-ASCII directories.
func TestShortenPath_MultibyteSafe(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"cyrillic", "/Users/иван/проекты/файл.go"},
		{"cjk", "/Users/田中/プロジェクト/file.go"},
		{"emoji", "/Users/test/🚀project/file.go"},
		{"mixed", "/var/привет/世界/test.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Try every reasonable maxLen — any one of them might land
			// inside a multibyte rune in the buggy version.
			for maxLen := 8; maxLen <= 40; maxLen++ {
				got := shortenPath(tc.path, maxLen)
				if !utf8.ValidString(got) {
					t.Fatalf("shortenPath(%q, %d) = %q — invalid UTF-8 (bytes: % x)",
						tc.path, maxLen, got, []byte(got))
				}
				if strings.Contains(got, string(utf8.RuneError)) {
					t.Fatalf("shortenPath(%q, %d) contains replacement char: %q",
						tc.path, maxLen, got)
				}
			}
		})
	}
}

func TestShortenPath_RuneCount(t *testing.T) {
	// maxLen is in runes, not bytes. A 30-rune Cyrillic path (60 bytes)
	// passed through with maxLen=40 should NOT be truncated.
	path := strings.Repeat("я", 30) // 30 runes, 60 bytes
	got := shortenPath(path, 40)
	if got != path {
		t.Errorf("30-rune path with maxLen=40 should pass through, got %q", got)
	}
}

func TestTruncateString_RuneAware(t *testing.T) {
	// Cyrillic — 2 bytes per rune.
	got := truncateString("привет мир", 5)
	if !utf8.ValidString(got) {
		t.Errorf("truncateString produced invalid UTF-8: %q", got)
	}
	if r := []rune(got); len(r) > 5 {
		t.Errorf("got %d runes, want <= 5: %q", len(r), got)
	}

	// Short input should pass through.
	if got := truncateString("hi", 10); got != "hi" {
		t.Errorf("short input changed: %q", got)
	}
}

func TestToolDisplayName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"read", "Read"},
		{"git_status", "Git Status"},
		{"web_fetch", "Web Fetch"},
		{"", ""},
		{"foo", "Foo"},
		{"a_b_c", "A B C"},
	}
	for _, tc := range cases {
		if got := toolDisplayName(tc.in); got != tc.want {
			t.Errorf("toolDisplayName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
