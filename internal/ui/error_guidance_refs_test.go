package ui

import (
	"strings"
	"testing"
)

// TestErrorGuidance_NoStaleSlashRefs guards against the audit-found
// regression: error guidance entries used to reference `/setup`,
// `/auth`, `/model list`, and `/config show api` — none of which are
// registered slash commands. Users hitting an auth failure would read
// "Try: /auth" and type a non-existent command, doubling their
// confusion at the worst possible moment.
//
// This test fails on any future entry that names a known dead alias.
// Add new dead patterns here as they're invented; better to spell out
// the blocklist than to grep over English suggestion text and miss
// edge cases.
func TestErrorGuidance_NoStaleSlashRefs(t *testing.T) {
	dead := []string{
		"/setup ", "/setup\n", "Run /setup", "Try: /setup",
		"/auth ", "/auth\n", "Run /auth", "Try: /auth",
		"/model list", // pre-v0.74 shape; current is just /model
		"/config show",
		"/oauth-login", // OAuth flows removed in v0.65
	}
	for _, g := range errorGuidancePatterns {
		hay := g.Title + " · " + strings.Join(g.Suggestions, " · ") + " · " + g.Command
		for _, needle := range dead {
			if strings.Contains(hay, needle) {
				t.Errorf("error guidance %q contains stale reference %q\n  full text: %s",
					g.Title, needle, hay)
			}
		}
	}
}

// TestErrorGuidance_CommandFieldShape asserts the Command field is
// either empty or starts with "/" so the TUI's "Try: %s" template
// reads naturally. A bare command name like "login" would render as
// "Try: login" which suggests typing literally that word.
func TestErrorGuidance_CommandFieldShape(t *testing.T) {
	for _, g := range errorGuidancePatterns {
		if g.Command == "" {
			continue
		}
		if !strings.HasPrefix(g.Command, "/") {
			t.Errorf("error guidance %q: Command=%q must start with '/' (or be empty)",
				g.Title, g.Command)
		}
	}
}
