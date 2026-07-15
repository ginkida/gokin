package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestCommandPaletteBackspaceDeletesOneVisibleCharacter(t *testing.T) {
	for _, value := range []string{"👍🏽", "e\u0301", "👩‍💻"} {
		t.Run(fmt.Sprintf("query/%x", []byte(value)), func(t *testing.T) {
			p := NewCommandPalette(DefaultStyles())
			p.SetQuery("find " + value)
			p.BackspaceQuery()
			if got := p.GetQuery(); got != "find " {
				t.Fatalf("BackspaceQuery split %q into %q", value, got)
			}
		})

		t.Run(fmt.Sprintf("argument/%x", []byte(value)), func(t *testing.T) {
			p := NewCommandPalette(DefaultStyles())
			p.AppendArg("find " + value)
			p.SetArgError("old validation error")
			p.BackspaceArg()
			if p.argValue != "find " || p.argError != "" {
				t.Fatalf("BackspaceArg split %q: value=%q error=%q", value, p.argValue, p.argError)
			}
		})
	}
}

func TestShortcutsSearchBackspaceDeletesOneVisibleCharacter(t *testing.T) {
	for _, value := range []string{"👍🏽", "e\u0301", "👩‍💻"} {
		t.Run(fmt.Sprintf("%x", []byte(value)), func(t *testing.T) {
			m := NewModel()
			m.shortcutsOverlay.Show()
			m.shortcutsOverlay.SetSearch("find " + value)
			m.state = StateShortcutsOverlay

			_ = m.handleShortcutsOverlayKeys(tea.KeyMsg{Type: tea.KeyBackspace})
			if got := m.shortcutsOverlay.GetSearch(); got != "find " {
				t.Fatalf("shortcuts Backspace split %q into %q", value, got)
			}
		})
	}
}

func TestCommandPaletteExactSlashQuerySelectsOnlySlashCommandAndHonorsAlias(t *testing.T) {
	m := NewModel()
	submitted := ""
	actionRuns := 0
	m.SetCallbacks(func(line string) { submitted = line }, nil)
	m.SetCommandAliases(map[string]string{"p": "plan"})
	m.commandPalette.commands = []EnhancedPaletteCommand{
		{Name: "plan", Shortcut: "Ctrl+X", Type: CommandTypeAction, Enabled: true, Action: func() { actionRuns++ }},
		{Name: "plan", Shortcut: "/plan", Type: CommandTypeSlash, Enabled: true},
	}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("/P")
	m.state = StateCommandPalette

	selected := m.commandPalette.GetSelected()
	if selected == nil || selected.Type != CommandTypeSlash || selected.Shortcut != "/plan" {
		t.Fatalf("slash alias selected %#v, want canonical slash command", selected)
	}
	if view := stripAnsi(m.commandPalette.View(60, 18)); !strings.Contains(view, "> /plan") {
		t.Fatalf("slash alias target is not visible before Enter:\n%s", view)
	}

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/plan" || actionRuns != 0 || m.state != StateProcessing {
		t.Fatalf("alias execution submitted=%q action-runs=%d state=%v", submitted, actionRuns, m.state)
	}
}

func TestCommandPaletteExactSlashAliasCanEnterRequiredArgumentStep(t *testing.T) {
	m := NewModel()
	m.SetCallbacks(func(string) {}, nil)
	m.SetCommandAliases(map[string]string{"o": "open"})
	m.commandPalette.commands = []EnhancedPaletteCommand{{
		Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true,
	}}
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("/o")
	m.state = StateCommandPalette

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.commandPalette.InArgEntry() || m.commandPalette.argCmd.Shortcut != "/open" {
		t.Fatalf("required alias did not enter canonical argument step: arg-entry=%v command=%q", m.commandPalette.InArgEntry(), m.commandPalette.argCmd.Shortcut)
	}
}

func TestShortcutsOverlayDegenerateGeometryNeverExceedsTerminal(t *testing.T) {
	for width := 1; width <= 8; width++ {
		for height := 1; height <= 8; height++ {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				overlay := NewShortcutsOverlay(DefaultStyles())
				overlay.Show()
				view := overlay.View(width, height)
				if got := lipgloss.Width(view); got > width {
					t.Fatalf("overlay width=%d exceeds %d:\n%s", got, width, stripAnsi(view))
				}
				if got := lipgloss.Height(view); got > height {
					t.Fatalf("overlay height=%d exceeds %d:\n%s", got, height, stripAnsi(view))
				}
				plain := stripAnsi(view)
				if strings.Contains(plain, "1–0/") {
					t.Fatalf("hidden list advertised an impossible visible range:\n%s", plain)
				}

				overlay.SetSearch("definitely-no-match")
				empty := overlay.View(width, height)
				if got := lipgloss.Width(empty); got > width {
					t.Fatalf("empty overlay width=%d exceeds %d:\n%s", got, width, stripAnsi(empty))
				}
				if got := lipgloss.Height(empty); got > height {
					t.Fatalf("empty overlay height=%d exceeds %d:\n%s", got, height, stripAnsi(empty))
				}
				emptyPlain := stripAnsi(empty)
				if !strings.Contains(emptyPlain, "Esc") && !strings.Contains(emptyPlain, "↔") {
					t.Fatalf("empty overlay lost recognizable clear/resize recovery:\n%s", emptyPlain)
				}
			})
		}
	}
}

func TestShortcutsOverlayLongSearchKeepsEditingTailVisible(t *testing.T) {
	overlay := NewShortcutsOverlay(DefaultStyles())
	overlay.Show()
	overlay.SetSearch(strings.Repeat("界", 30) + "needle")

	view := stripAnsi(overlay.View(44, 14))
	if !strings.Contains(view, "needle_") {
		t.Fatalf("long shortcut search hid its editing tail and cursor:\n%s", view)
	}
}

func TestShortcutEntryDegenerateWidthDoesNotOverflow(t *testing.T) {
	for width := 1; width <= 4; width++ {
		line := renderShortcutEntry(Shortcut{Keys: []string{"Ctrl", "Shift", "P"}, Description: "Open palette"}, width)
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("width=%d shortcut row width=%d: %q", width, got, stripAnsi(line))
		}
	}
}

func TestCommandPaletteDegenerateGeometryNeverExceedsTerminal(t *testing.T) {
	for width := 1; width <= 8; width++ {
		for height := 1; height <= 8; height++ {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				p := NewCommandPalette(DefaultStyles())
				p.commands = []EnhancedPaletteCommand{{
					Name: "help", Shortcut: "/help", Description: "Show help", Type: CommandTypeSlash, Enabled: true,
				}}
				p.visible = true
				p.filterCommands("")
				view := p.View(width, height)
				if got := lipgloss.Width(view); got > width {
					t.Fatalf("palette width=%d exceeds %d:\n%s", got, width, stripAnsi(view))
				}
				if got := lipgloss.Height(view); got > height {
					t.Fatalf("palette height=%d exceeds %d:\n%s", got, height, stripAnsi(view))
				}
			})
		}
	}
}

func TestCommandPaletteArgumentEntryDegenerateGeometryNeverExceedsTerminal(t *testing.T) {
	for width := 1; width <= 8; width++ {
		for height := 1; height <= 8; height++ {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				p := NewCommandPalette(DefaultStyles())
				p.visible = true
				p.BeginArgEntry(EnhancedPaletteCommand{Name: "open", Shortcut: "/open", ArgHint: "<file>", Type: CommandTypeSlash, Enabled: true})
				p.AppendArg("path/to/file")
				view := p.View(width, height)
				if got := lipgloss.Width(view); got > width {
					t.Fatalf("argument palette width=%d exceeds %d:\n%s", got, width, stripAnsi(view))
				}
				if got := lipgloss.Height(view); got > height {
					t.Fatalf("argument palette height=%d exceeds %d:\n%s", got, height, stripAnsi(view))
				}
			})
		}
	}
}

func TestCommandPaletteInlineErrorRequiresRoomBeforeEnterCanRun(t *testing.T) {
	submitted := ""
	m := argEntryPaletteModel(&submitted)
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 5})
	m.commandPalette.SetArgError("Previous validation failed")

	view := stripAnsi(m.commandPalette.View(20, 5))
	if (!strings.Contains(view, "Resize") && !strings.Contains(view, "↔")) || strings.Contains(view, "↵") {
		t.Fatalf("error-crowded target advertised execution:\n%s", view)
	}
	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "" || !m.commandPalette.InArgEntry() || m.commandPalette.argError != paletteArgumentResizeError {
		t.Fatalf("hidden error target submitted=%q arg-entry=%v error=%q", submitted, m.commandPalette.InArgEntry(), m.commandPalette.argError)
	}
}

func TestCommandPaletteRegularErrorFrameFitsTerminal(t *testing.T) {
	for _, query := range []string{"", "command"} {
		for _, height := range []int{14, 15, 18} {
			t.Run(fmt.Sprintf("query=%q/height=%d", query, height), func(t *testing.T) {
				p := NewCommandPalette(DefaultStyles())
				p.commands = paletteGeometryCommands(30)
				p.visible = true
				p.SetQuery(query)
				p.SetSubmitError("Unavailable: command submission is not connected")

				view := p.View(60, height)
				assertPaletteGeometry(t, view, 60, height)
				if plain := stripAnsi(view); !strings.Contains(plain, "Submission unavailable") || !strings.Contains(plain, "Esc Close") {
					t.Fatalf("error frame lost status or recovery:\n%s", plain)
				}

				p.TogglePreview()
				preview := p.View(60, height)
				assertPaletteGeometry(t, preview, 60, height)
				if plain := stripAnsi(preview); !strings.Contains(plain, "unavailable") || !strings.Contains(plain, "Tab List") {
					t.Fatalf("error preview lost status or return action:\n%s", plain)
				}
			})
		}
	}
}
