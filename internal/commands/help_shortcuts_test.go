package commands

import (
	"context"
	"strings"
	"testing"
)

// TestHelpCommand_ShortcutsListIsCurrent pins that the /help shortcuts
// table mentions every binding the welcome panel + shortcuts overlay
// teach. Pre-v0.84.6 the /help table was stuck at the pre-v0.84.0
// roster — missing Ctrl+K (model selector, v0.84.0), Ctrl+E (expand
// tool output, v0.84.3), Ctrl+H (context observatory), Alt+C (copy),
// Ctrl+U/Ctrl+D (half-page scroll), and `?` (shortcuts overlay).
//
// If a future binding is added to internal/ui/shortcuts.go but the /help
// shortcut subset is forgotten, this test catches the divergence.
func TestHelpCommand_ShortcutsListIsCurrent(t *testing.T) {
	h := &HelpCommand{handler: NewHandler()}
	out, err := h.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}

	mustContain := []string{
		"Ctrl+P",    // Command palette
		"Ctrl+K",    // Model selector — v0.84.0
		"Ctrl+E",    // Expand tool output — v0.84.3
		"Ctrl+H",    // Context Observatory
		"Ctrl+T",    // Task list
		"Ctrl+O",    // Activity feed
		"Ctrl+U",    // Half page up
		"Ctrl+D",    // Half page down
		"Alt+C",     // Copy
		"Shift+Tab", // Cycle mode
		"Esc",       // Cancel
		"?",         // Shortcuts overlay
	}
	for _, key := range mustContain {
		if !strings.Contains(out, key) {
			t.Errorf("/help output missing binding %q", key)
		}
	}

	// Stale wording must not return.
	staleStrings := []string{
		"Toggle mouse mode", // Ctrl+G is "select mode", not "mouse mode"
	}
	for _, stale := range staleStrings {
		if strings.Contains(out, stale) {
			t.Errorf("/help output contains stale text %q", stale)
		}
	}
}
