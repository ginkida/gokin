package ui

import (
	"strings"
	"testing"
)

func TestThemeHintDoesNotPromiseUnavailableSwitching(t *testing.T) {
	hint := strings.ToLower(NewModel().getCommandHint("/theme"))
	if !strings.Contains(hint, "show") || !strings.Contains(hint, "graphite") {
		t.Fatalf("theme hint does not describe the read-only command: %q", hint)
	}
	if strings.Contains(hint, "switch") {
		t.Fatalf("theme hint promises unavailable switching: %q", hint)
	}
}
