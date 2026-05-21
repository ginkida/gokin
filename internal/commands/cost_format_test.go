package commands

import (
	"strings"
	"testing"
)

// TestCostCommand_FormatTokensUppercaseK pins that /cost renders token
// counts with uppercase K (matching internal/ui/tui_tokens.go's
// formatTokens, which the status bar uses). Previously /cost emitted
// lowercase "1.5k" while the status bar showed "1.5K" for the same
// value — a small inconsistency that read as a typo when you glanced
// from one to the other.
//
// If a future refactor reverts to lowercase k, this test trips.
func TestCostCommand_FormatTokensUppercaseK(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{1_000, "1.0K"},
		{1_500, "1.5K"},
		{128_000, "128.0K"},
		{1_000_000, "1.0M"},
		{2_500_000, "2.5M"},
	}
	for _, tc := range cases {
		got := formatTokens(tc.n)
		if got != tc.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
		// Inverse pin: lowercase k must never appear in the output.
		if strings.Contains(got, "k") {
			t.Errorf("formatTokens(%d) = %q contains lowercase k — must use uppercase K to match status bar", tc.n, got)
		}
	}
}
