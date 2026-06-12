package commands

import (
	"context"
	"strings"
	"testing"
)

// TestShortcutsCommand_ListIsCurrent pins that the /shortcuts command
// output mentions every binding the welcome panel + shortcuts overlay
// teach. Same pattern as TestHelpCommand_ShortcutsListIsCurrent —
// /shortcuts is the OTHER flat-text fallback (the filterable overlay
// at `?` is authoritative).
//
// Pre-v0.84.8 the /shortcuts table was stuck at the pre-v0.84.0 roster
// — same drift as /help had. Missing: Ctrl+K (model selector, v0.84.0),
// Ctrl+E (expand tool output, v0.84.3). Stale wording: "Option+C"
// instead of "Alt+C" (terminal binding), "Background tasks" instead of
// "Toggle task list", "Toggle plan mode" instead of "Cycle Normal/
// Plan/YOLO".
func TestShortcutsCommand_ListIsCurrent(t *testing.T) {
	out, err := (&ShortcutsCommand{}).Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}

	mustContain := []string{
		"Ctrl+P",     // Command palette
		"Ctrl+K",     // Model selector — added v0.84.0
		"Ctrl+E",     // Expand tool output — added v0.84.3
		"Ctrl+H",     // Context Observatory
		"Ctrl+T",     // Task list
		"Ctrl+O",     // Activity feed
		"Ctrl+U",     // Half page up
		"Ctrl+D",     // Half page down
		"Alt+C",      // Copy last response (terminal binding name)
		"Shift+Tab",  // Cycle mode
		"?",          // Filterable overlay
		"task list",  // Ctrl+T description (no longer "Background tasks")
		"Cycle mode", // Shift+Tab description (no longer "Toggle plan mode")
	}
	for _, key := range mustContain {
		if !strings.Contains(out, key) {
			t.Errorf("/shortcuts output missing %q", key)
		}
	}

	// Stale wording must not return.
	staleStrings := []string{
		"Option+C",         // terminal binding is Alt+C
		"Background tasks", // Ctrl+T shows the task list, not "background tasks"
		"Toggle plan mode", // Shift+Tab cycles through 3 modes, not 2
	}
	for _, stale := range staleStrings {
		if strings.Contains(out, stale) {
			t.Errorf("/shortcuts output contains stale text %q", stale)
		}
	}
}
