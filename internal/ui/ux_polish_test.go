package ui

import (
	"strings"
	"testing"
)

func TestToggleStateLabel(t *testing.T) {
	if got := toggleStateLabel("Todos", true); got != "Todos shown" {
		t.Errorf("on: got %q, want 'Todos shown'", got)
	}
	if got := toggleStateLabel("Activity feed", false); got != "Activity feed hidden" {
		t.Errorf("off: got %q, want 'Activity feed hidden'", got)
	}
}

// The shortcuts overlay is user-visible documentation — every binding it lists
// must be a real handler. These panel/display toggles are wired in tui.go
// handleGlobalKeys; this pins that they stay documented (the inverse — a
// documented-but-dead shortcut — is the honesty bug this whole pass targets).
func TestDefaultShortcuts_DocumentRealPanelToggles(t *testing.T) {
	var all string
	for _, c := range DefaultShortcuts() {
		for _, s := range c.Shortcuts {
			all += strings.Join(s.Keys, "+") + "\n"
		}
	}
	for _, want := range []string{"Ctrl+O", "Ctrl+A", "Ctrl+T", "Ctrl+L", "Ctrl+J"} {
		if !strings.Contains(all, want) {
			t.Errorf("shortcuts overlay missing %q — it's a real binding and should be documented", want)
		}
	}
	// The INVERSE pin: Ctrl+Shift+C must NOT be advertised. bubbletea v1 has
	// no "ctrl+shift+c" key name (only home/end/arrows get ctrl+shift), and a
	// real terminal encodes the chord as byte 0x03 — identical to Ctrl+C, the
	// CANCEL key. The overlay listing it steered users into cancelling their
	// own in-flight request. (This test previously asserted the binding was
	// real — a stale pin on a physically unreachable handler.)
	if strings.Contains(all, "Ctrl+Shift+C") {
		t.Error("shortcuts overlay must not advertise Ctrl+Shift+C — the chord is indistinguishable from Ctrl+C (cancel) in a terminal")
	}
}

func TestDefaultShortcutsDescribeAppendOnlyToolOutputHonestly(t *testing.T) {
	var descriptions []string
	for _, category := range DefaultShortcuts() {
		if category.Name != "Command Center" {
			continue
		}
		for _, shortcut := range category.Shortcuts {
			key := strings.Join(shortcut.Keys, "+")
			if key == "Ctrl+E" || key == "E" {
				descriptions = append(descriptions, shortcut.Description)
			}
		}
	}
	joined := strings.Join(descriptions, "\n")
	for _, want := range []string{"existing scrollback", "new tool outputs"} {
		if !strings.Contains(strings.ToLower(joined), want) {
			t.Fatalf("tool-output shortcuts do not explain %q:\n%s", want, joined)
		}
	}
	if strings.Contains(strings.ToLower(joined), "collapse all") {
		t.Fatalf("shortcuts still promise an impossible scrollback collapse:\n%s", joined)
	}
}
