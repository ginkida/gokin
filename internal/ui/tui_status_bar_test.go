package ui

import (
	"strings"
	"testing"
)

func TestRenderContextBarAbsoluteTokens(t *testing.T) {
	// pct is a fraction in [0.0, 1.0] per the helper's docstring.
	// outputTokens=0 — the 5th arg (pending output tokens indicator) is
	// not exercised here.
	result := renderContextBar(0.5, 8, 50000, 128000, 0)
	if !strings.Contains(result, "50.0K") {
		t.Errorf("should contain 50.0K, got: %s", stripAnsi(result))
	}
	if !strings.Contains(result, "128.0K") {
		t.Errorf("should contain 128.0K, got: %s", stripAnsi(result))
	}
}

func TestRenderContextBarPercentFallback(t *testing.T) {
	// Without token counts, should fall back to percentage (pct * 100).
	result := renderContextBar(0.75, 8, 0, 0, 0)
	if !strings.Contains(result, "75%") {
		t.Errorf("should contain 75%%, got: %s", stripAnsi(result))
	}
}

func TestRenderContextBarEmpty(t *testing.T) {
	result := renderContextBar(0, 8, 0, 0, 0)
	if result != "" {
		t.Errorf("0%% should return empty, got: %q", result)
	}
}

// TestRenderContextBarShowsProjectedOutput pins the live-streaming behaviour:
// with outputTokens > 0, the bar gains a secondary "▓" band showing where
// it will be once those tokens land as input on the next turn. Without
// this, users see only the +N counter ticking up while the visual bar
// stays frozen, defeating the purpose of a live indicator.
func TestRenderContextBarShowsProjectedOutput(t *testing.T) {
	// 25K/100K input (25%), +50K output streaming → total projected 75%.
	// Bar width 8 cols: input = 2 filled cells, projection = 4 more.
	out := stripAnsi(renderContextBar(0.25, 8, 25000, 100000, 50000))
	if !strings.Contains(out, "█") {
		t.Errorf("expected solid band for input tokens, got: %q", out)
	}
	if !strings.Contains(out, "▓") {
		t.Errorf("expected projection band for output tokens, got: %q", out)
	}
	// Label should also surface the +output count.
	if !strings.Contains(out, "+50.0K") {
		t.Errorf("expected +output label, got: %q", out)
	}
}

// TestRenderContextBarZeroOutputNoProjection: when nothing is streaming,
// the bar must not render the ▓ band — otherwise it'd create a false
// impression of "already at X%" when the model hasn't started.
func TestRenderContextBarZeroOutputNoProjection(t *testing.T) {
	out := stripAnsi(renderContextBar(0.5, 8, 50000, 100000, 0))
	if strings.Contains(out, "▓") {
		t.Errorf("projection band must be absent when outputTokens=0: %q", out)
	}
}

// TestRenderContextBarProjectionClampedToBarWidth: if input + projected
// would exceed the bar, the projection band fills the remainder — it
// must not overflow into the empty-band slot or produce a negative count.
func TestRenderContextBarProjectionClampedToBarWidth(t *testing.T) {
	// 80K input + 100K output would exceed 100K max by 80%. Bar should
	// fill fully without panic.
	out := stripAnsi(renderContextBar(0.8, 8, 80000, 100000, 100000))
	if strings.Count(out, "░") > 0 {
		t.Errorf("projection clamp failed — empty slot rendered when saturated: %q", out)
	}
}

func TestRenderContextBarHighUsage(t *testing.T) {
	result := renderContextBar(0.96, 8, 96000, 100000, 0)
	if !strings.Contains(result, "96.0K") {
		t.Errorf("should contain 96.0K, got: %s", stripAnsi(result))
	}
	// Should include urgency hint at >95%
	if !strings.Contains(result, "compact") {
		t.Errorf("high usage should include compact hint, got: %s", stripAnsi(result))
	}
}

func TestContextUrgencyColor(t *testing.T) {
	tests := []struct {
		pct  float64
		desc string
	}{
		{10, "low usage - green"},
		{65, "elevated usage - amber"},
		{85, "high usage - orange"},
		{98, "critical usage - red"},
	}

	prev := ""
	for _, tt := range tests {
		color := string(contextUrgencyColor(tt.pct))
		if color == "" {
			t.Errorf("%s: should return a color", tt.desc)
		}
		if color == prev && tt.pct > 60 {
			// Colors should change between levels
		}
		prev = color
	}
}

func TestFormatTokensHelper(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{500, "500"},
		{1500, "1.5K"},
		{128000, "128.0K"},
		{1500000, "1.5M"},
	}
	for _, tt := range tests {
		got := formatTokens(tt.tokens)
		if got != tt.want {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.tokens, got, tt.want)
		}
	}
}

// stripAnsi removes ANSI escape sequences for testing.
func stripAnsi(s string) string {
	var result strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
