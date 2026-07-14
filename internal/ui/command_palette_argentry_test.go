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

func TestPaletteRequiredArgRejectsEmptySubmissionInline(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})
	m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{
		Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true,
	})
	m.commandPalette.visible = true
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "" {
		t.Fatalf("empty required argument submitted %q", submitted)
	}
	if !m.commandPalette.InArgEntry() || m.state != StateCommandPalette {
		t.Fatalf("empty required argument closed palette: arg=%v state=%v", m.commandPalette.InArgEntry(), m.state)
	}
	for _, size := range []struct{ width, height int }{{10, 6}, {60, 18}} {
		view := m.commandPalette.View(size.width, size.height)
		assertPaletteGeometry(t, view, size.width, size.height)
		if plain := stripAnsi(view); !strings.Contains(plain, "Argument required") && size.width >= 20 {
			t.Fatalf("validation error missing at %dx%d:\n%s", size.width, size.height, plain)
		}
	}

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("main.go")})
	if m.commandPalette.argError != "" {
		t.Fatalf("typing did not clear validation error: %q", m.commandPalette.argError)
	}
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/open main.go" {
		t.Fatalf("valid argument submitted %q", submitted)
	}
}

func TestPaletteArgEntryQuotesSingleFileArg(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})

	m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true})
	m.state = StateCommandPalette
	m.commandPalette.visible = true

	for _, r := range "space file.go" {
		_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})

	if submitted != `/open "space file.go"` {
		t.Fatalf("submitted = %q, want quoted single file arg", submitted)
	}
}

func TestPaletteDirectSlashLineWithArgsSubmitsWithoutArgEntry(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})

	m.commandPalette.commands = []EnhancedPaletteCommand{
		{Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true},
	}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery(`/open "space file.go"`)
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != `/open "space file.go"` {
		t.Fatalf("submitted = %q, want full slash line", submitted)
	}
	if m.commandPalette.InArgEntry() {
		t.Fatal("full slash line should submit directly, not enter arg-entry")
	}
	if m.state != StateProcessing {
		t.Fatalf("state after direct slash submit = %v, want StateProcessing", m.state)
	}
}

func TestPaletteDirectSlashLineHonorsAliases(t *testing.T) {
	m := NewModel()
	var submitted string
	m.SetCallbacks(func(s string) { submitted = s }, func() {})
	m.SetCommandAliases(map[string]string{"p": "plan"})

	m.commandPalette.commands = []EnhancedPaletteCommand{
		{Name: "plan", Shortcut: "/plan", Type: CommandTypeSlash, Enabled: true},
	}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("/P status")
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/P status" {
		t.Fatalf("submitted = %q, want alias slash line preserved", submitted)
	}
	if m.state != StateProcessing {
		t.Fatalf("state after alias slash submit = %v, want StateProcessing", m.state)
	}
}

func TestFormatPaletteArgEntryValue(t *testing.T) {
	open := EnhancedPaletteCommand{Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true}
	blame := EnhancedPaletteCommand{Name: "blame", Shortcut: "/blame", ArgHint: "<file> [N|N-M|N M]", Type: CommandTypeSlash, Enabled: true}
	grep := EnhancedPaletteCommand{Name: "grep", Shortcut: "/grep", ArgHint: "<pattern> [path]", Type: CommandTypeSlash, Enabled: true}

	cases := []struct {
		name string
		cmd  EnhancedPaletteCommand
		in   string
		want string
	}{
		{"plain file", open, "main.go", "main.go"},
		{"space file", open, "space file.go", `"space file.go"`},
		{"apostrophe file", open, "John's file.go", `"John's file.go"`},
		{"double quote file", open, `draft "v2".go`, `"draft \"v2\".go"`},
		{"already double quoted", open, `"space file.go"`, `"space file.go"`},
		{"already single quoted", open, `'space file.go'`, `'space file.go'`},
		{"file plus single line", blame, "space file.go 12", `"space file.go" 12`},
		{"file plus dashed range", blame, "space file.go 12-20", `"space file.go" 12-20`},
		{"file plus spaced range", blame, "space file.go 12 20", `"space file.go" 12 20`},
		{"file only for optional range command", blame, "space file.go", `"space file.go"`},
		{"preserve repeated spaces in file", blame, "space  file.go 12", `"space  file.go" 12`},
		{"multi arg command untouched", grep, "foo bar", "foo bar"},
	}

	for _, tc := range cases {
		if got := formatPaletteArgEntryValue(tc.cmd, tc.in); got != tc.want {
			t.Fatalf("%s: formatPaletteArgEntryValue(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
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
		paletteActionNotifications: false,
		paletteActionTodos:         false,
		paletteActionActivityFeed:  false,
		paletteActionLiveDetail:    false,
		paletteActionAgentTree:     false,
		paletteActionObservatory:   false,
		paletteActionPlanPanel:     false,
		paletteActionPlanningMode:  false,
		paletteActionClearScreen:   false,
		paletteActionCompactMode:   false,
	}
	seenIDs := make(map[string]string, len(wantIDs))
	for _, c := range m.commandPalette.commands {
		if c.Type != CommandTypeAction {
			continue
		}
		if c.Action != nil {
			t.Errorf("built-in action %q still uses a closure; it must use ActionID for live dispatch", c.Name)
		}
		if c.ActionID == "" {
			t.Errorf("built-in action %q has no ActionID", c.Name)
			continue
		}
		if previous, duplicate := seenIDs[c.ActionID]; duplicate {
			t.Errorf("built-in actions %q and %q share ActionID %q", previous, c.Name, c.ActionID)
		}
		seenIDs[c.ActionID] = c.Name
		if _, ok := wantIDs[c.ActionID]; !ok {
			t.Errorf("unexpected built-in palette ActionID %q for %q", c.ActionID, c.Name)
		} else {
			wantIDs[c.ActionID] = true
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Errorf("expected a registered palette action with ActionID %q", id)
		}
	}
}
