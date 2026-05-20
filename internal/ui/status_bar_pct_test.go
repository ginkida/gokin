package ui

import (
	"strings"
	"testing"
)

// TestCompactStatusPercent_FractionRendersAsRealPercent pins the bug fixed in
// the v0.83 follow-up: `fmt.Sprintf("ctx:%.0f%%", pct)` rendered a fraction
// like 0.42 as "ctx:0%" because the format string didn't multiply by 100.
// `tokenUsage.PercentUsed` is a 0.0–1.0 fraction (see context/tokens.go:302),
// and every status-bar code path that prints it must multiply by 100.
//
// If a future refactor drops the `*100`, this test trips with "got: ctx:0%
// want substring 42%".
func TestCompactStatusPercent_FractionRendersAsRealPercent(t *testing.T) {
	cases := []struct {
		name        string
		percentUsed float64
		wantPct     string
	}{
		{"5pct", 0.05, "5%"},
		{"42pct", 0.42, "42%"},
		{"87pct", 0.87, "87%"},
		{"99pct", 0.99, "99%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				showTokens: true,
				tokenUsage: &TokenUsageMsg{
					PercentUsed: tc.percentUsed,
					MaxTokens:   100_000, // non-zero so showTokens-gated branches enter
					Tokens:      int(float64(100_000) * tc.percentUsed),
				},
			}
			parts := m.minimalStatusSegments()
			joined := strings.Join(parts, "|")
			if !strings.Contains(joined, tc.wantPct) {
				t.Errorf("minimalStatusSegments() = %q, want substring %q (regression of pct*100 fix)", joined, tc.wantPct)
			}
		})
	}
}

// TestStatusBarPercent_ZeroFractionShowsZero ensures we don't accidentally
// render "ctx:NaN%" or strip the segment entirely when PercentUsed is 0
// (fresh session, no tokens consumed yet).
func TestStatusBarPercent_ZeroFractionShowsZero(t *testing.T) {
	m := Model{
		showTokens: true,
		tokenUsage: &TokenUsageMsg{
			PercentUsed: 0,
			MaxTokens:   100_000,
			Tokens:      0,
		},
	}
	parts := m.minimalStatusSegments()
	joined := strings.Join(parts, "|")
	if !strings.Contains(joined, "0%") {
		t.Errorf("zero-usage fraction should render `0%%`, got: %q", joined)
	}
}

// TestStatusBarPercent_ShowTokensDisabledHidesSegment verifies the gate that
// replaced the old `pct > 0` check: when the user has disabled token display
// (showTokens=false), no `ctx:...` segment should appear even if tokenUsage
// is populated.
func TestStatusBarPercent_ShowTokensDisabledHidesSegment(t *testing.T) {
	m := Model{
		showTokens: false,
		tokenUsage: &TokenUsageMsg{
			PercentUsed: 0.42,
			MaxTokens:   100_000,
			Tokens:      42_000,
		},
	}
	parts := m.minimalStatusSegments()
	joined := strings.Join(parts, "|")
	if strings.Contains(joined, "ctx:") || strings.Contains(joined, "%") {
		t.Errorf("showTokens=false should suppress the ctx segment, got: %q", joined)
	}
}
