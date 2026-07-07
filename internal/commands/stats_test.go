package commands

import "testing"

// TestFormatNumber_NegativeNumbers (round 6) pins the fix: formatNumber's
// digit-grouping loop counted the leading '-' as a digit, so a comma could
// land right after the sign whenever the digit count was a multiple of 3
// (e.g. -123 -> "-,123", -123456 -> "-,123,456"). Reachable in practice via
// context/metrics.go's TotalTokensSaved, which can go negative if a
// compaction pass produces a larger summary than the original — rendered by
// /stats through this function.
func TestFormatNumber_NegativeNumbers(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{5, "5"},
		{-5, "-5"},
		{123, "123"},
		{-123, "-123"},
		{1234, "1,234"},
		{-1234, "-1,234"},
		{123456, "123,456"},
		{-123456, "-123,456"},
		{1234567, "1,234,567"},
		{-1234567, "-1,234,567"},
	}
	for _, tt := range cases {
		if got := formatNumber(tt.n); got != tt.want {
			t.Errorf("formatNumber(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
