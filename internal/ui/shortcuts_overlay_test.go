package ui

import (
	"strings"
	"testing"
)

func TestShortcutsOverlayDimensionsFitSmallTerminals(t *testing.T) {
	widthCases := []struct {
		width int
		want  int
	}{
		{0, 80},
		{100, 80},
		{60, 56},
		{8, 8},
		{3, 3},
	}
	for _, tc := range widthCases {
		if got := shortcutsOverlayWidth(tc.width); got != tc.want {
			t.Fatalf("shortcutsOverlayWidth(%d) = %d, want %d", tc.width, got, tc.want)
		}
	}

	heightCases := []struct {
		height int
		want   int
	}{
		{0, 25},
		{40, 25},
		{20, 16},
		{8, 8},
		{3, 3},
	}
	for _, tc := range heightCases {
		if got := shortcutsOverlayHeight(tc.height); got != tc.want {
			t.Fatalf("shortcutsOverlayHeight(%d) = %d, want %d", tc.height, got, tc.want)
		}
	}
}

func TestShortcutsOverlayFooterMentionsQClose(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.Show()

	// The footer must advertise Esc as the close key and must NOT claim "q"
	// closes: the overlay is filter-first now — every typed rune (q/k/j
	// included) belongs to the search query, because the old letter-key
	// interceptors made the advertised free-text filter unable to contain
	// them (typing "quit" dismissed the overlay on its first letter).
	got := stripAnsi(overlay.View(100, 30))
	if !strings.Contains(got, "Esc close") {
		t.Fatalf("shortcuts footer should mention Esc close:\n%s", got)
	}
	if strings.Contains(got, "q close") {
		t.Fatalf("shortcuts footer must not claim q closes (filter-first):\n%s", got)
	}
}
