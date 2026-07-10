package ui

import (
	"strings"
	"testing"
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
		{18, 20, 14},
		{8, 20, 10},
		{18, 0, 14},
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
