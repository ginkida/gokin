package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPaletteNeedsArg pins the trigger heuristic: only a slash command with a
// REQUIRED arg ("<...>") drops into inline arg-entry; optional ("[...]"),
// parameterless, and action commands run on a single Enter.
func TestPaletteNeedsArg(t *testing.T) {
	cases := []struct {
		name string
		cmd  EnhancedPaletteCommand
		want bool
	}{
		{"required", EnhancedPaletteCommand{Type: CommandTypeSlash, Enabled: true, ArgHint: "<pattern>"}, true},
		{"required-with-optional", EnhancedPaletteCommand{Type: CommandTypeSlash, Enabled: true, ArgHint: "<file> [N]"}, true},
		{"optional-only", EnhancedPaletteCommand{Type: CommandTypeSlash, Enabled: true, ArgHint: "[count]"}, false},
		{"parameterless", EnhancedPaletteCommand{Type: CommandTypeSlash, Enabled: true, ArgHint: ""}, false},
		{"disabled", EnhancedPaletteCommand{Type: CommandTypeSlash, Enabled: false, ArgHint: "<x>"}, false},
		{"action", EnhancedPaletteCommand{Type: CommandTypeAction, Enabled: true, ArgHint: "<x>"}, false},
	}
	for _, tc := range cases {
		if got := PaletteNeedsArg(tc.cmd); got != tc.want {
			t.Errorf("PaletteNeedsArg(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPaletteArgEntryFlow drives the full keyboard flow: picking a required-arg
// command enters arg-entry, typed text (with a space) is collected, and Enter
// submits the assembled "/name args" line exactly once.
func TestPaletteArgEntryFlow(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})

	m.commandPalette.commands = []EnhancedPaletteCommand{
		{Name: "grep", Description: "Search files", Shortcut: "/grep", ArgHint: "<pattern> [path]", Type: CommandTypeSlash, Enabled: true},
	}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("grep") // filters -> selects /grep
	m.state = StateCommandPalette

	// Enter on a required-arg command -> arg-entry (does NOT submit yet).
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.commandPalette.InArgEntry() {
		t.Fatal("Enter on a required-arg command should open inline arg-entry")
	}
	if submitted != "" {
		t.Fatalf("nothing should be submitted yet, got %q", submitted)
	}

	// Type "foo bar" (space must register inside arg-entry).
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("foo")})
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeySpace})
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("bar")})

	// Render must not panic and should show the typed value.
	if got := stripAnsi(m.commandPalette.View(80, 24)); !strings.Contains(got, "foo bar") {
		t.Fatalf("arg-entry view missing typed value:\n%s", got)
	}

	// Enter submits "/grep foo bar" and leaves arg-entry.
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/grep foo bar" {
		t.Fatalf("submitted = %q, want /grep foo bar", submitted)
	}
	if m.commandPalette.InArgEntry() || m.commandPalette.IsVisible() {
		t.Fatal("palette should close after arg-entry submit")
	}
	if m.state != StateProcessing {
		t.Fatalf("state after submit = %v, want StateProcessing", m.state)
	}
}

// TestPaletteArgEntryEsc returns to the command list without submitting.
func TestPaletteArgEntryEsc(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})

	m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true})
	m.state = StateCommandPalette
	m.commandPalette.visible = true

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.commandPalette.InArgEntry() {
		t.Fatal("Esc should leave arg-entry")
	}
	if submitted != "" {
		t.Fatalf("Esc must not submit, got %q", submitted)
	}
}

// TestPaletteOptionalArgRunsBare pins that an optional-arg command still runs on
// a single Enter (the common no-typing path is not slowed down).
func TestPaletteOptionalArgRunsBare(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})

	m.commandPalette.commands = []EnhancedPaletteCommand{
		{Name: "log", Shortcut: "/log", ArgHint: "[count]", Type: CommandTypeSlash, Enabled: true},
	}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("log")
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.commandPalette.InArgEntry() {
		t.Fatal("optional-arg command should not enter arg-entry")
	}
	if submitted != "/log" {
		t.Fatalf("submitted = %q, want /log (ran bare)", submitted)
	}
}

// TestPaletteSettingsActionDispatchesOnLiveModel pins the value-receiver fix:
// the "Open Settings" action mutates/calls through the LIVE model via
// dispatchPaletteAction, not a detached registration-time closure.
func TestPaletteSettingsActionDispatchesOnLiveModel(t *testing.T) {
	m := NewModel()
	opened := false
	m.SetOpenSettingsCallback(func() { opened = true })
	m.RegisterPaletteActions()

	m.commandPalette.Show()
	m.commandPalette.SetQuery("open settings")
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !opened {
		t.Fatal("Open Settings palette action should invoke the settings callback on the live model")
	}
}

// TestRegisterPaletteActionsUsesIDs guards that the built-in actions route
// through ActionID (live dispatch), not registration-time closures that would
// mutate a detached Model copy in production.
func TestRegisterPaletteActionsUsesIDs(t *testing.T) {
	m := NewModel()
	m.RegisterPaletteActions()
	m.commandPalette.RefreshCommands()

	wantIDs := map[string]bool{
		paletteActionSettings:      false,
		paletteActionModelSelector: false,
		paletteActionShortcuts:     false,
	}
	for _, c := range m.commandPalette.commands {
		if c.Type != CommandTypeAction {
			continue
		}
		if c.Action != nil {
			t.Errorf("built-in action %q still uses a closure; it must use ActionID for live dispatch", c.Name)
		}
		if _, ok := wantIDs[c.ActionID]; ok {
			wantIDs[c.ActionID] = true
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("expected a registered palette action with ActionID %q", id)
		}
	}
}
