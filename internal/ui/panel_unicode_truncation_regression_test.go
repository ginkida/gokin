package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

func TestTruncateForWidthPreservesExtendedGraphemeClustersAndCellBudget(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{name: "zero", input: "anything", width: 0, want: ""},
		{name: "one", input: "anything", width: 1, want: "…"},
		{name: "emoji modifier", input: "A👍🏽BC", width: 4, want: "A👍🏽…"},
		{name: "combining mark", input: "Ae\u0301BC", width: 3, want: "Ae\u0301…"},
		{name: "zwj emoji", input: "A👩‍💻BC", width: 4, want: "A👩‍💻…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateForWidth(tt.input, tt.width)
			if got != tt.want {
				t.Fatalf("truncateForWidth(%q, %d)=%q, want %q", tt.input, tt.width, got, tt.want)
			}
			if width := lipgloss.Width(got); width > tt.width {
				t.Fatalf("rendered width=%d, want <=%d: %q", width, tt.width, got)
			}
			if tt.width <= 0 {
				if got != "" {
					t.Fatalf("non-positive width returned %q", got)
				}
				return
			}
			plain := ansi.Strip(got)
			graphemes := uniseg.NewGraphemes(plain)
			for graphemes.Next() {
				cluster := graphemes.Str()
				if cluster == "…" {
					continue
				}
				if !strings.Contains(tt.input, cluster) {
					t.Fatalf("returned a partial grapheme %q from %q", cluster, tt.input)
				}
			}
		})
	}
}

func TestTruncateForWidthPreservesStyledSGRBoundary(t *testing.T) {
	styled := "\x1b[1;31mA👩‍💻BC\x1b[0m"
	got := truncateForWidth(styled, 4)
	if width := lipgloss.Width(got); width != 4 {
		t.Fatalf("styled truncation width=%d, want 4: %q", width, got)
	}
	if plain := ansi.Strip(got); plain != "A👩‍💻…" {
		t.Fatalf("styled truncation split a ZWJ grapheme: %q", plain)
	}
	if strings.Contains(ansi.Strip(got), "\x1b") {
		t.Fatalf("stripped output retained an escape fragment: %q", got)
	}
	// Rendering a sentinel after the truncated string must not inherit the
	// source style. Lip Gloss emits reset sequences at the end of its render;
	// ansi.Truncate must retain those trailing non-printing controls.
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("truncation dropped the final SGR reset: %q", got)
	}
}
