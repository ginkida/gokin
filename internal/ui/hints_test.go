package ui

import (
	"strings"
	"testing"
	"time"
)

func TestGeneralHintsMatchCurrentBindings(t *testing.T) {
	h := NewHintSystem(DefaultStyles())

	var seen []string
	for range 8 {
		h.lastHintTime = time.Now().Add(-time.Minute)
		seen = append(seen, h.GetContextualHint(StateInput, "", 10*time.Minute))
	}
	joined := strings.Join(seen, "\n")

	if strings.Contains(joined, "Option+C") {
		t.Fatalf("hints should use terminal binding Alt+C, got:\n%s", joined)
	}
	if strings.Contains(joined, "track background tasks") {
		t.Fatalf("Ctrl+T hint should describe task list, got:\n%s", joined)
	}
	if !strings.Contains(joined, "Alt+C") {
		t.Fatalf("copy hint missing Alt+C:\n%s", joined)
	}
	if !strings.Contains(joined, "task list") {
		t.Fatalf("task-list hint missing:\n%s", joined)
	}
}
