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
	for _, want := range []string{"Ctrl+O", "Ctrl+A", "Ctrl+T", "Ctrl+L", "Ctrl+J", "Ctrl+Shift+C"} {
		if !strings.Contains(all, want) {
			t.Errorf("shortcuts overlay missing %q — it's a real binding and should be documented", want)
		}
	}
}
