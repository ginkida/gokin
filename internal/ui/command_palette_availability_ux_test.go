package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func setPaletteCommandsForTest(m *Model, commands ...EnhancedPaletteCommand) {
	m.commandPalette.commands = append([]EnhancedPaletteCommand(nil), commands...)
	m.commandPalette.visible = true
	m.commandPalette.SetQuery("")
	m.state = StateCommandPalette
}

func TestCommandPaletteSlashAvailabilityMatchesHandlerFooterAndStatus(t *testing.T) {
	slash := EnhancedPaletteCommand{Name: "help", Shortcut: "/help", Enabled: true, Type: CommandTypeSlash}
	m := NewModel()
	m.width, m.height = 80, 24
	setPaletteCommandsForTest(m, slash)

	view := stripAnsi(m.commandPalette.View(m.width, m.height))
	if !strings.Contains(view, "Submission unavailable") || strings.Contains(view, "Enter Run") {
		t.Fatalf("unlinked slash command advertised execution:\n%s", view)
	}
	assertShortcutHints(t, m,
		[]string{"Tab Details", "esc Close"},
		[]string{"Enter Run", "↑↓ Navigate"},
	)

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateCommandPalette || m.commandPalette.GetQuery() != "" || m.commandPalette.submitError == "" {
		t.Fatalf("unavailable Enter lost palette state: state=%v query=%q error=%q", m.state, m.commandPalette.GetQuery(), m.commandPalette.submitError)
	}
	assertShortcutHints(t, m, []string{"esc Close"}, []string{"Enter Run", "↑↓ Navigate"})

	m.SetCallbacks(func(string) {}, nil)
	m.commandPalette.SetQuery("")
	view = stripAnsi(m.commandPalette.View(m.width, m.height))
	if !strings.Contains(view, "Enter Run") || strings.Contains(view, "Submission unavailable") {
		t.Fatalf("linked slash command did not recover execution:\n%s", view)
	}
	assertShortcutHints(t, m, []string{"Enter Run", "Tab Details", "esc Close"}, []string{"↑↓ Navigate"})
}

func TestCommandPaletteDirectSlashAvailabilityIsHonestBeforeEnter(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	setPaletteCommandsForTest(m, EnhancedPaletteCommand{Name: "open", Shortcut: "/open", Enabled: true, Type: CommandTypeSlash})
	m.commandPalette.SetQuery("/open main.go")

	view := stripAnsi(m.commandPalette.View(m.width, m.height))
	for _, want := range []string{"Submission unavailable for /open", "Connect command submission", "Backspace Edit", "Esc Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("unlinked direct command missing %q:\n%s", want, view)
		}
	}
	for _, unavailable := range []string{"Ready to run", "Press Enter to run", "Enter Run"} {
		if strings.Contains(view, unavailable) {
			t.Fatalf("unlinked direct command advertised %q:\n%s", unavailable, view)
		}
	}
	assertShortcutHints(t, m,
		[]string{"Backspace Edit", "esc Close"},
		[]string{"Enter Run"},
	)

	m.SetCallbacks(func(string) {}, nil)
	view = stripAnsi(m.commandPalette.View(m.width, m.height))
	for _, want := range []string{"Ready to run /open", "Press Enter to run", "Enter Run"} {
		if !strings.Contains(view, want) {
			t.Fatalf("linked direct command missing %q:\n%s", want, view)
		}
	}
	assertShortcutHints(t, m, []string{"Enter Run", "Backspace Edit", "esc Close"}, nil)
}

func TestCommandPaletteArgEntryNeverReadvertisesUnavailableSubmit(t *testing.T) {
	for _, width := range []int{20, 32, 40, 44, 80} {
		t.Run(itoa(width), func(t *testing.T) {
			m := NewModel()
			m.width, m.height = width, 14
			m.state = StateCommandPalette
			m.commandPalette.visible = true
			m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{
				Name: "open", Shortcut: "/open", ArgHint: "<file>", Enabled: true, Type: CommandTypeSlash,
			})

			view := stripAnsi(m.commandPalette.View(m.width, m.height))
			if strings.Contains(view, "Enter Run") {
				t.Fatalf("width=%d unavailable arg entry readvertised Enter:\n%s", width, view)
			}
			if !strings.Contains(view, "Esc") {
				t.Fatalf("width=%d unavailable arg entry lost recovery:\n%s", width, view)
			}
			assertShortcutHints(t, m, []string{"esc Back"}, []string{"Enter Run"})
		})
	}
}

func TestCommandPaletteActionAndPreviewRemainAvailableWithoutSubmitLink(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	setPaletteCommandsForTest(m, EnhancedPaletteCommand{Name: "settings", Shortcut: "Ctrl+S", Enabled: true, Type: CommandTypeAction})

	assertShortcutHints(t, m, []string{"Enter Run", "Tab Details", "esc Close"}, nil)
	m.commandPalette.TogglePreview()
	assertShortcutHints(t, m, []string{"Enter Run", "Tab List", "esc Close"}, nil)
}
