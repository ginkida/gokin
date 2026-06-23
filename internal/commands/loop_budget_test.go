package commands

import (
	"strings"
	"testing"
)

func TestParseTokenBudget(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"500000", 500000, true},
		{"200k", 200000, true},
		{"2m", 2_000_000, true},
		{"1.5m", 1_500_000, true},
		{"  100K ", 100000, true},
		{"0", 0, false},
		{"-5", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{"k", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseTokenBudget(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("parseTokenBudget(%q) = (%d, %v), want (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseLoopStartFlags(t *testing.T) {
	// Flag mid-args, value separate — stripped from rest, one opt produced.
	opts, rest, errMsg := parseLoopStartFlags([]string{"--max-tokens", "200k", "5m", "fix", "bugs"})
	if errMsg != "" || len(opts) != 1 || strings.Join(rest, " ") != "5m fix bugs" {
		t.Errorf("separate-value: opts=%d rest=%v err=%q", len(opts), rest, errMsg)
	}

	// `=` form.
	opts, rest, errMsg = parseLoopStartFlags([]string{"--max-tokens=2m", "fix"})
	if errMsg != "" || len(opts) != 1 || strings.Join(rest, " ") != "fix" {
		t.Errorf("=-form: opts=%d rest=%v err=%q", len(opts), rest, errMsg)
	}

	// No flag — args pass through unchanged.
	opts, rest, errMsg = parseLoopStartFlags([]string{"fix", "the", "bug"})
	if errMsg != "" || len(opts) != 0 || strings.Join(rest, " ") != "fix the bug" {
		t.Errorf("no-flag: opts=%d rest=%v err=%q", len(opts), rest, errMsg)
	}

	// Missing value → error.
	if _, _, errMsg = parseLoopStartFlags([]string{"--max-tokens"}); errMsg == "" {
		t.Error("missing --max-tokens value should error")
	}

	// Bad value → error.
	if _, _, errMsg = parseLoopStartFlags([]string{"--max-tokens", "abc", "fix"}); errMsg == "" {
		t.Error("invalid --max-tokens value should error")
	}
}
