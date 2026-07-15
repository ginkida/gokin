package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestCommandPaletteWidthFitsNarrowTerminals(t *testing.T) {
	cases := []struct {
		width int
		want  int
	}{
		{100, 70},
		{60, 54},
		{50, 48},
		{32, 30},
		{10, 10},
	}

	for _, tc := range cases {
		if got := commandPaletteWidth(tc.width); got != tc.want {
			t.Fatalf("commandPaletteWidth(%d) = %d, want %d", tc.width, got, tc.want)
		}
	}
}

func TestCommandPaletteHeightFitsShortTerminals(t *testing.T) {
	cases := []struct {
		height    int
		maxHeight int
		want      int
	}{
		{0, 20, 20},
		{40, 20, 20},
		{18, 20, 16},
		{8, 20, 6},
		{18, 0, 16},
	}

	for _, tc := range cases {
		if got := commandPaletteHeight(tc.height, tc.maxHeight); got != tc.want {
			t.Fatalf("commandPaletteHeight(%d, %d) = %d, want %d", tc.height, tc.maxHeight, got, tc.want)
		}
	}
}

func TestCommandPaletteSearchRanksDirectNameFirst(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{
		{
			Name:        "Open Model Selector",
			Description: "Switch the active AI model",
			Shortcut:    "Ctrl+K",
			Priority:    10,
			Type:        CommandTypeAction,
		},
		{
			Name:        "model",
			Description: "Switch AI model",
			Shortcut:    "/model",
			Priority:    100,
			Type:        CommandTypeSlash,
		},
	}

	p.SetQuery("model")
	if len(p.filtered) < 2 {
		t.Fatalf("expected both model commands, got %d", len(p.filtered))
	}
	if got := p.filtered[0].Name; got != "model" {
		t.Fatalf("top command = %q, want direct slash command", got)
	}
}

func TestCommandPaletteNoMatchesGivesRecoveryHint(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{{Name: "help", Description: "Show help", Shortcut: "/help"}}
	p.Show()
	p.SetQuery("zzzz")

	got := stripAnsi(p.View(80, 24))
	for _, want := range []string{`No matches for "zzzz"`, "/help", "Backspace"} {
		if !strings.Contains(got, want) {
			t.Fatalf("palette no-match view missing %q:\n%s", want, got)
		}
	}
}

func TestCommandPaletteFooterReflectsAvailableActions(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{
		{Name: "help", Description: "Show help", Shortcut: "/help", Enabled: true},
		{Name: "locked", Description: "Unavailable action", Shortcut: "/locked", Enabled: false, Reason: "setup required"},
	}
	p.visible = true

	p.SetQuery("missing")
	got := stripAnsi(p.View(80, 24))
	if strings.Contains(got, "Navigate") || strings.Contains(got, "Enter Run") || strings.Contains(got, "Tab Details") {
		t.Fatalf("empty results advertise unavailable actions:\n%s", got)
	}
	for _, want := range []string{"Backspace Edit", "Esc Close"} {
		if !strings.Contains(got, want) {
			t.Fatalf("empty-result footer missing %q:\n%s", want, got)
		}
	}

	p.SetQuery("locked")
	got = stripAnsi(p.View(80, 24))
	if strings.Contains(got, "Enter Run") {
		t.Fatalf("disabled selection advertises execution:\n%s", got)
	}
	for _, want := range []string{"\u00d7", "setup required", "Tab Details"} {
		if !strings.Contains(got, want) {
			t.Fatalf("disabled selection missing %q:\n%s", want, got)
		}
	}
}

func TestCommandPalettePreviewFollowsSelectionAndSearch(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{
		{Name: "first", Description: "First command", Shortcut: "/first", Enabled: true},
		{Name: "second", Description: "Second command", Shortcut: "/second", Enabled: true},
	}
	p.visible = true
	p.filterCommands("")
	p.TogglePreview()

	if p.previewCmd == nil || p.previewCmd.Name != "first" {
		t.Fatalf("initial preview = %#v, want first", p.previewCmd)
	}
	p.SelectNext()
	if p.previewCmd == nil || p.previewCmd.Name != "second" {
		t.Fatalf("preview after navigation = %#v, want second", p.previewCmd)
	}
	p.SetQuery("first")
	if p.previewCmd == nil || p.previewCmd.Name != "first" {
		t.Fatalf("preview after search = %#v, want first", p.previewCmd)
	}
	p.SetQuery("missing")
	if p.previewCmd != nil {
		t.Fatalf("preview survived empty results: %#v", p.previewCmd)
	}
}

func TestCommandPaletteDirectSlashArgsRenderAsRunnable(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{{Name: "open", Shortcut: "/open", Type: CommandTypeSlash, Enabled: true}}
	p.visible = true
	p.SetQuery(`/open "space file.go"`)

	got := stripAnsi(p.View(80, 24))
	for _, want := range []string{"Ready to run /open with arguments", "Press Enter to run this command", "Enter Run"} {
		if !strings.Contains(got, want) {
			t.Fatalf("direct command view missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "No matches") || strings.Contains(got, "0 results") {
		t.Fatalf("runnable direct command rendered as empty search:\n%s", got)
	}
}

func TestCommandPaletteDirectSlashLineWithArgs(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{
		{Name: "open", Shortcut: "/open", Type: CommandTypeSlash, Enabled: true},
		{Name: "plan", Shortcut: "/plan", Type: CommandTypeSlash, Enabled: true},
		{Name: "disabled", Shortcut: "/disabled", Type: CommandTypeSlash, Enabled: false},
	}
	p.SetCommandAliases(map[string]string{"P": "Plan"})

	p.SetQuery(`/open "space file.go"`)
	got, ok := p.DirectSlashLineWithArgs()
	if !ok || got != `/open "space file.go"` {
		t.Fatalf("DirectSlashLineWithArgs = (%q,%v), want full /open line", got, ok)
	}
	if p.IsVisible() || p.InArgEntry() {
		t.Fatal("direct slash execution should hide and clear palette state")
	}

	p.SetQuery("/open")
	if got, ok := p.DirectSlashLineWithArgs(); ok || got != "" {
		t.Fatalf("exact /open should not bypass arg-entry, got (%q,%v)", got, ok)
	}

	p.SetQuery("/p status")
	got, ok = p.DirectSlashLineWithArgs()
	if !ok || got != "/p status" {
		t.Fatalf("alias slash line = (%q,%v), want /p status direct submit", got, ok)
	}

	p.SetQuery("/disabled arg")
	if got, ok := p.DirectSlashLineWithArgs(); ok || got != "" {
		t.Fatalf("disabled command should not direct-execute, got (%q,%v)", got, ok)
	}
}

func paletteGeometryCommands(count int) []EnhancedPaletteCommand {
	commands := make([]EnhancedPaletteCommand, count)
	for i := range commands {
		commands[i] = EnhancedPaletteCommand{
			Name:        "command-" + itoa(i+1),
			Description: "A deliberately long command description with Unicode 界面 and details",
			Usage:       "/command [arguments]\nsecond usage line\nthird usage line",
			Shortcut:    "/command-" + itoa(i+1),
			Category:    PaletteCategoryInfo{Name: "Category " + itoa(i+1)},
			Enabled:     true,
			Type:        CommandTypeSlash,
		}
	}
	return commands
}

func assertPaletteGeometry(t *testing.T, view string, width, height int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("%dx%d palette line width=%d: %q", width, height, got, ansi.Strip(line))
		}
	}
	if got := renderedLineCount(view); got > height {
		t.Fatalf("%dx%d palette height=%d:\n%s", width, height, got, ansi.Strip(view))
	}
}

func TestCommandPaletteViewFitsGeometryMatrix(t *testing.T) {
	for _, size := range []struct{ width, height int }{
		{10, 6}, {20, 8}, {32, 12}, {60, 18}, {80, 24},
	} {
		p := NewCommandPalette(DefaultStyles())
		p.commands = paletteGeometryCommands(30)
		p.visible = true
		p.filterCommands("")
		view := p.View(size.width, size.height)
		assertPaletteGeometry(t, view, size.width, size.height)
		plain := ansi.Strip(view)
		for _, want := range []string{"Esc"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("%dx%d compact palette lost %q:\n%s", size.width, size.height, want, plain)
			}
		}
		if !strings.Contains(plain, "Cmds") && !strings.Contains(plain, "Command Palette") {
			t.Fatalf("%dx%d palette lost its title:\n%s", size.width, size.height, plain)
		}

		p.TogglePreview()
		assertPaletteGeometry(t, p.View(size.width, size.height), size.width, size.height)
	}
}

func TestCommandPaletteArgEntryFitsGeometryMatrix(t *testing.T) {
	for _, size := range []struct{ width, height int }{{10, 6}, {20, 8}, {32, 12}, {60, 18}} {
		p := NewCommandPalette(DefaultStyles())
		p.visible = true
		p.BeginArgEntry(EnhancedPaletteCommand{
			Name: "open", Shortcut: "/open", Description: strings.Repeat("long description ", 8), ArgHint: "<file>", Enabled: true,
		})
		p.AppendArg("a very long path/界面/file.go")
		view := p.View(size.width, size.height)
		assertPaletteGeometry(t, view, size.width, size.height)
		if plain := ansi.Strip(view); !strings.Contains(plain, "Esc") {
			t.Fatalf("%dx%d arg entry lost recovery key:\n%s", size.width, size.height, plain)
		}
	}
}

func TestCommandPalettePageAndBoundaryNavigationUsesRenderedCapacity(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = paletteGeometryCommands(30)
	p.visible = true
	p.filterCommands("")
	p.SetSize(60, 18)

	p.PageDown()
	if p.selected != 7 {
		t.Fatalf("PageDown selected=%d, want one rendered page (7 commands + overflow row)", p.selected)
	}
	p.SelectLast()
	if p.selected != 29 {
		t.Fatalf("SelectLast selected=%d, want 29", p.selected)
	}
	if plain := ansi.Strip(p.View(60, 18)); !strings.Contains(plain, "> /command-30") {
		t.Fatalf("last selection is outside rendered viewport:\n%s", plain)
	}
	p.SelectFirst()
	if p.selected != 0 || p.scroll != 0 {
		t.Fatalf("SelectFirst left selected=%d scroll=%d", p.selected, p.scroll)
	}
}

func TestCommandPaletteMiddleScrollKeepsIndicatorsAndFooterInsideHeight(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = paletteGeometryCommands(30)
	p.visible = true
	p.filterCommands("")
	p.SetSize(60, 14)
	p.selected = 15
	p.adjustScroll()

	view := p.View(60, 14)
	plain := ansi.Strip(view)
	assertPaletteGeometry(t, view, 60, 14)
	for _, want := range []string{"↑ ", "↓ ", "> /command-16", "Esc Close"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("middle palette page missing %q:\n%s", want, plain)
		}
	}
}

func TestCommandPaletteResizeReanchorsSelectionWithinRenderedWindow(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = paletteGeometryCommands(30)
	p.visible = true
	p.filterCommands("")
	p.SetSize(80, 40)
	p.selected = 18
	p.adjustScroll()

	view := p.View(60, 14)
	plain := ansi.Strip(view)
	assertPaletteGeometry(t, view, 60, 14)
	if !strings.Contains(plain, "> /command-19") {
		t.Fatalf("resize left selected command outside the viewport:\n%s", plain)
	}
}

func TestCommandPaletteSanitizesCatalogAndTypedControlSequences(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.SetActionCommands([]EnhancedPaletteCommand{{
		Name:        "Unsafe\nAction",
		Description: "Description\x1b[2J\nFORGED",
		Usage:       "one\ntwo\x1b[31m\nthree",
		Shortcut:    "Ctrl+U\nFORGED",
		Category:    PaletteCategoryInfo{Name: "Tools\nFORGED"},
		Reason:      "Reason\nFORGED",
		Enabled:     true,
		Type:        CommandTypeAction,
	}})
	p.Show()
	if len(p.commands) != 1 {
		t.Fatalf("sanitized action catalog has %d commands, want 1", len(p.commands))
	}
	for _, value := range []string{p.commands[0].Name, p.commands[0].Description, p.commands[0].Shortcut, p.commands[0].Category.Name} {
		if strings.ContainsAny(value, "\r\n\x1b") {
			t.Fatalf("catalog retained visual control sequence: %q", value)
		}
	}

	p.SetQuery("Unsafe\n\x1b[2JAction")
	if strings.ContainsAny(p.GetQuery(), "\r\n\x1b") {
		t.Fatalf("query retained visual control sequence: %q", p.GetQuery())
	}
	p.BeginArgEntry(EnhancedPaletteCommand{Name: "open", Shortcut: "/open"})
	p.AppendArg("a  b\n\x1b[2Jc")
	if p.argValue != "a  b c" {
		t.Fatalf("argument sanitization=%q, want spaces preserved without controls", p.argValue)
	}
}

func TestCommandPaletteLongUnicodeInputKeepsEditingTailVisible(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{{Name: "help", Description: "Show help", Shortcut: "/help", Enabled: true}}
	p.visible = true
	query := strings.Repeat("界", 40) + "needle"
	p.SetQuery(query)

	view := p.View(60, 14)
	plain := stripAnsi(view)
	assertPaletteGeometry(t, view, 60, 14)
	if !strings.Contains(plain, "needle_") {
		t.Fatalf("long query hid its editing tail and cursor:\n%s", plain)
	}
}

func TestCommandPaletteLongMetadataOutranksDescription(t *testing.T) {
	p := NewCommandPalette(DefaultStyles())
	p.commands = []EnhancedPaletteCommand{
		{Name: "open", Shortcut: "/open", Description: strings.Repeat("descriptive 界面 ", 10), ArgHint: "<very-long-required-file-path>", Enabled: true},
		{Name: "locked", Shortcut: "/locked", Description: strings.Repeat("decorative details ", 10), Reason: "configure provider credentials first", Enabled: false},
	}
	p.visible = true
	p.filterCommands("")

	first := stripAnsi(p.View(60, 18))
	assertPaletteGeometry(t, p.View(60, 18), 60, 18)
	if !strings.Contains(first, "<very") || !strings.Contains(first, "path>") {
		t.Fatalf("required argument was displaced by description:\n%s", first)
	}
	p.SelectNext()
	second := stripAnsi(p.View(60, 18))
	if !strings.Contains(second, "configure") || !strings.Contains(second, "first)") {
		t.Fatalf("disabled reason was displaced by description:\n%s", second)
	}
}

func TestCommandPaletteLongArgEntryKeepsEditingTailVisible(t *testing.T) {
	for _, size := range []struct{ width, height int }{{20, 8}, {60, 18}} {
		p := NewCommandPalette(DefaultStyles())
		p.visible = true
		p.BeginArgEntry(EnhancedPaletteCommand{Name: "open", Shortcut: "/open", ArgHint: "<file>", Enabled: true})
		p.AppendArg(strings.Repeat("界", 30) + "main.go")

		view := p.View(size.width, size.height)
		plain := stripAnsi(view)
		assertPaletteGeometry(t, view, size.width, size.height)
		if !strings.Contains(plain, "main.go_") {
			t.Fatalf("%dx%d long argument hid its editing tail and cursor:\n%s", size.width, size.height, plain)
		}
	}
}
