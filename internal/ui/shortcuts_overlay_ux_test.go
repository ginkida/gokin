package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestShortcutsOverlayRenderedGeometryIncludesChrome(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.Show()

	for _, size := range []struct{ width, height int }{{100, 30}, {60, 20}, {30, 16}} {
		view := overlay.View(size.width, size.height)
		wantWidth := shortcutsOverlayWidth(size.width)
		wantHeight := shortcutsOverlayHeight(size.height)
		if got := lipgloss.Width(view); got != wantWidth {
			t.Errorf("%dx%d overlay width=%d, want %d", size.width, size.height, got, wantWidth)
		}
		if got := lipgloss.Height(view); got != wantHeight {
			t.Errorf("%dx%d overlay height=%d, want %d", size.width, size.height, got, wantHeight)
		}
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Errorf("%dx%d row %d width=%d exceeds terminal:\n%s", size.width, size.height, row, got, stripAnsi(view))
			}
		}
	}
}

func TestShortcutsOverlayScrollsWithinLargeCategoryAndKeepsFooterVisible(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.categories = []ShortcutCategory{{Name: "Large", Shortcuts: make([]Shortcut, 14)}}
	for i := range overlay.categories[0].Shortcuts {
		overlay.categories[0].Shortcuts[i] = Shortcut{Keys: []string{fmt.Sprintf("K%d", i)}, Description: fmt.Sprintf("Action %d", i)}
	}
	overlay.Show()

	initial := stripAnsi(overlay.View(60, 20))
	if !strings.Contains(initial, "Action 0") || !strings.Contains(initial, "Esc close") {
		t.Fatalf("initial large-category window lost content or footer:\n%s", initial)
	}
	for range 8 {
		overlay.ScrollDown()
	}
	scrolled := stripAnsi(overlay.View(60, 20))
	for _, want := range []string{"Action 8", "↑/↓ 9–", "Esc close"} {
		if !strings.Contains(scrolled, want) {
			t.Fatalf("scrolled shortcuts window missing %q:\n%s", want, scrolled)
		}
	}
	if strings.Contains(scrolled, "Action 0") {
		t.Fatalf("scrolling within one category did not move the row window:\n%s", scrolled)
	}
}

func TestShortcutsOverlayNoMatchesKeepsRecoveryVisible(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.Show()
	overlay.SetSearch("definitely-no-such-shortcut")

	view := stripAnsi(overlay.View(50, 16))
	for _, want := range []string{"No matching shortcuts found.", "Keep typing", "Esc clear"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no-match shortcuts view missing %q:\n%s", want, view)
		}
	}
}

func TestShortcutEntryTruncatesDescriptionToAvailableWidth(t *testing.T) {
	line := renderShortcutEntry(Shortcut{Keys: []string{"Ctrl", "Shift", "P"}, Description: strings.Repeat("long description ", 8)}, 28)
	if got := lipgloss.Width(line); got > 28 {
		t.Fatalf("shortcut row width=%d, want <=28: %q", got, stripAnsi(line))
	}
}

func TestShortcutsOverlayPageAndBoundaryNavigation(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.categories = []ShortcutCategory{{Name: "Large", Shortcuts: make([]Shortcut, 20)}}
	for i := range overlay.categories[0].Shortcuts {
		overlay.categories[0].Shortcuts[i] = Shortcut{Keys: []string{fmt.Sprintf("K%d", i)}, Description: fmt.Sprintf("Action %d", i)}
	}
	overlay.Show()
	_ = overlay.View(60, 20) // Establish the height-aware page size.

	overlay.PageDown()
	if overlay.scrollIndex <= 1 {
		t.Fatalf("PageDown advanced only to %d", overlay.scrollIndex)
	}
	overlay.ScrollToEnd()
	if overlay.scrollIndex != 19 {
		t.Fatalf("End index=%d, want 19", overlay.scrollIndex)
	}
	if view := stripAnsi(overlay.View(60, 20)); !strings.Contains(view, "Action 19") || !strings.Contains(view, "Esc close") {
		t.Fatalf("end window lost last action or close recovery:\n%s", view)
	}
	overlay.ScrollToStart()
	if overlay.scrollIndex != 0 {
		t.Fatalf("Home index=%d, want 0", overlay.scrollIndex)
	}
}

func TestShortcutsOverlayReopensFreshAndSanitizesPastedSearch(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.Show()
	overlay.SetSearch("\x1b[31mmodel\nselector\x00")
	if got := overlay.GetSearch(); got != "model selector" {
		t.Fatalf("sanitized search=%q, want %q", got, "model selector")
	}
	view := overlay.View(44, 14)
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > shortcutsOverlayWidth(44) {
			t.Fatalf("sanitized overlay row %d width=%d:\n%s", row, got, stripAnsi(view))
		}
	}

	overlay.Hide()
	overlay.Show()
	if got := overlay.GetSearch(); got != "" {
		t.Fatalf("reopened overlay retained stale search %q", got)
	}
	if plain := stripAnsi(overlay.View(44, 14)); strings.Contains(plain, "Filter:") {
		t.Fatalf("reopened overlay rendered stale filter:\n%s", plain)
	}
}
