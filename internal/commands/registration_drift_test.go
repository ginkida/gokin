package commands

import (
	"sort"
	"strings"
	"testing"

	"gokin/internal/ui"
)

// TestEveryRegisteredCommandIsInAutocomplete is the structural guard that the
// /diff /log /branches /grep /blame autocomplete-gap (fixed in v0.78.12)
// can't recur silently. The pattern was: a command was added via
// h.Register(&FooCommand{}) and worked when typed, but was missing from
// ui.DefaultCommands() so it never appeared in the suggestion menu.
//
// This test cross-references the two sources at runtime. New commands
// either have to be added to both, or explicitly listed in
// hiddenFromAutocomplete below with a one-line reason.
func TestEveryRegisteredCommandIsInAutocomplete(t *testing.T) {
	// Commands that intentionally don't show in autocomplete. Each entry
	// MUST have a comment justifying the omission — silent drift is the
	// whole bug class this test exists to prevent.
	hiddenFromAutocomplete := map[string]string{
		// Aliases / shorthand: the canonical command is in autocomplete,
		// the alias resolves at parse time so showing both creates noise.
		"cost": "alias for /stats — shown via /stats entry instead",
		// Internal/debug commands: surfaced through other means (key chord,
		// dev-only flow). Suggesting them in autocomplete would expose
		// internals to end users.
		"debug-dump": "developer-only diagnostic; not for end-user autocomplete",
		"insights":   "internal coordinator output; surfaced via /stats integration",
		"checkpoints": "managed by /checkpoint key chord, not typed",
		// Legacy / soft-deprecated: kept registered for backwards-compat
		// but discouraged in new autocomplete UX.
		"keys": "shortcuts shown via /shortcuts; /keys remains as legacy alias",
	}

	// Build the autocomplete name set.
	autocomplete := make(map[string]bool)
	for _, c := range ui.DefaultCommands() {
		autocomplete[c.Name] = true
	}

	// Build the registered name set via the real handler.
	h := NewHandler()
	registered := h.ListCommands()

	var missing []string
	for _, c := range registered {
		name := c.Name()
		if autocomplete[name] {
			continue
		}
		if _, allowed := hiddenFromAutocomplete[name]; allowed {
			continue
		}
		missing = append(missing, name)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("commands registered in commands.NewHandler() but missing from ui.DefaultCommands():\n  %s\n\n"+
			"Either:\n"+
			"  1. Add them to defaultCommands() in internal/ui/input.go (preferred — gives autocomplete + arg hints), OR\n"+
			"  2. Add them to hiddenFromAutocomplete in this test with a one-line reason for the omission.",
			strings.Join(missing, "\n  "))
	}
}

// TestNoStaleAutocompleteEntries — the reverse direction: every name in
// DefaultCommands should resolve to a real registered command (or its
// alias). Catches the case where a command was deleted but its
// autocomplete entry was forgotten, leaving users to tab-complete a
// dead command.
func TestNoStaleAutocompleteEntries(t *testing.T) {
	h := NewHandler()

	// Resolve aliases too — `/p` autocompletes to /plan, so "p" entries
	// would still be valid even though only /plan is in ListCommands.
	resolves := func(name string) bool {
		if _, ok := h.GetCommand(name); ok {
			return true
		}
		// h.Parse honors aliases — Parse returns (name, args, isCommand).
		// If the resolved name is non-empty, it found a real command.
		if resolved, _, ok := h.Parse("/" + name); ok && resolved != "" {
			return true
		}
		return false
	}

	// Some autocomplete entries are namespace prefixes (e.g. "instructions"
	// is shown but actually surfaces a different system). Those are
	// listed here with a justification.
	allowedStale := map[string]string{
		"instructions": "surfaced via app handler, not registered as a Command",
		"reasoning":    "shorthand for /model reasoning — handled inline",
		"quickstart":   "shown via /help mode, not a standalone Command",
		"register-agent-type":   "agent registry sub-command, separate handler",
		"unregister-agent-type": "agent registry sub-command, separate handler",
		"list-agent-types":      "agent registry sub-command, separate handler",
	}

	var stale []string
	for _, c := range ui.DefaultCommands() {
		if resolves(c.Name) {
			continue
		}
		if _, allowed := allowedStale[c.Name]; allowed {
			continue
		}
		stale = append(stale, c.Name)
	}

	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("autocomplete entries in ui.DefaultCommands() that don't resolve to a registered command:\n  %s\n\n"+
			"Either remove them from defaultCommands() in internal/ui/input.go, or add them to allowedStale here with a reason.",
			strings.Join(stale, "\n  "))
	}
}
