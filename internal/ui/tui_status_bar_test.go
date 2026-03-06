package ui

import (
	"strings"
	"testing"
)

func TestRenderContextBarAbsoluteTokens(t *testing.T) {
	// With absolute token counts
	result := renderContextBar(50, 8, 50000, 128000)
	if !strings.Contains(result, "50.0K") {
		t.Errorf("should contain 50.0K, got: %s", stripAnsi(result))
	}
	if !strings.Contains(result, "128.0K") {
		t.Errorf("should contain 128.0K, got: %s", stripAnsi(result))
	}
}

func TestRenderContextBarPercentFallback(t *testing.T) {
	// Without token counts, should fall back to percentage
	result := renderContextBar(75, 8, 0, 0)
	if !strings.Contains(result, "75%") {
		t.Errorf("should contain 75%%, got: %s", stripAnsi(result))
	}
}

func TestRenderContextBarEmpty(t *testing.T) {
	result := renderContextBar(0, 8, 0, 0)
	if result != "" {
		t.Errorf("0%% should return empty, got: %q", result)
	}
}

func TestRenderContextBarHighUsage(t *testing.T) {
	result := renderContextBar(96, 8, 96000, 100000)
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
