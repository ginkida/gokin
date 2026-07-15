package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func hiddenActionPaletteModel(runs *int) *Model {
	m := NewModel()
	m.commandPalette.SetActionCommands([]EnhancedPaletteCommand{{
		Name:     "irreversible",
		Shortcut: "/irreversible",
		Type:     CommandTypeAction,
		Enabled:  true,
		Action:   func() { *runs++ },
	}})
	m.commandPalette.Show()
	m.state = StateCommandPalette
	return m
}

func assertPaletteResizeHints(t *testing.T, m *Model, recovery string) {
	t.Helper()
	resize, escape := false, false
	for _, hint := range m.contextualShortcutHintPairs() {
		if hint.key == "Enter" || hint.key == "↵" {
			t.Fatalf("hidden palette action still advertises Enter: %+v", m.contextualShortcutHintPairs())
		}
		resize = resize || hint.key == "↔"
		escape = escape || (hint.key == "esc" && hint.desc == recovery)
	}
	if !resize || !escape {
		t.Fatalf("hidden palette action hints lack Resize/Esc %s: %+v", recovery, m.contextualShortcutHintPairs())
	}
}

func TestCommandPaletteBlocksEnterWhileSelectedTargetIsBelowViewport(t *testing.T) {
	for width := 1; width <= 20; width++ {
		for height := 1; height <= 4; height++ {
			t.Run(fmt.Sprintf("%dx%d", width, height), func(t *testing.T) {
				runs := 0
				m := hiddenActionPaletteModel(&runs)
				m.applyResize(&tea.WindowSizeMsg{Width: width, Height: height})

				// In compact rendering the filter outranks the selected row; below
				// five rows the executable target is deliberately cropped away.
				if view := stripAnsi(m.View()); strings.Contains(view, "/irreversible") {
					t.Fatalf("setup: target unexpectedly visible at %dx%d:\n%s", width, height, view)
				}
				if footer := stripAnsi(m.commandPalette.View(width, height)); strings.Contains(footer, "↵") {
					t.Fatalf("hidden target advertised Enter at %dx%d:\n%s", width, height, footer)
				}
				assertPaletteResizeHints(t, m, "Close")
				_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})

				if runs != 0 {
					t.Fatalf("Enter executed a hidden palette target %d time(s)", runs)
				}
				if m.state != StateCommandPalette || m.commandPalette.submitError == "" {
					t.Fatalf("blocked Enter lost recovery state: state=%v error=%q", m.state, m.commandPalette.submitError)
				}
			})
		}
	}
}

func TestCommandPaletteBlocksUnreadablyNarrowSelectedTarget(t *testing.T) {
	for width := 1; width < compactPaletteListTargetMinWidth; width++ {
		runs := 0
		m := hiddenActionPaletteModel(&runs)
		m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
		_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if runs != 0 || m.state != StateCommandPalette {
			t.Errorf("%dx12 unreadable target executed: runs=%d state=%v", width, runs, m.state)
		}
	}
}

func TestCommandPaletteReadableBoundaryStillExecutes(t *testing.T) {
	runs := 0
	m := hiddenActionPaletteModel(&runs)
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 5})
	view := stripAnsi(m.View())
	if !strings.Contains(view, "/irrevers") {
		t.Fatalf("setup: readable boundary lost selected target:\n%s", view)
	}

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if runs != 1 || m.state != StateInput {
		t.Fatalf("readable palette target did not execute: runs=%d state=%v", runs, m.state)
	}
}

func argEntryPaletteModel(submitted *string) *Model {
	m := NewModel()
	m.SetCallbacks(func(line string) { *submitted = line }, nil)
	m.commandPalette.Show()
	m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{
		Name: "grep", Shortcut: "/grep", ArgHint: "<pattern>", Type: CommandTypeSlash, Enabled: true,
	})
	m.commandPalette.AppendArg("needle")
	m.state = StateCommandPalette
	return m
}

func TestCommandPaletteArgEntryBlocksHiddenCommandAndField(t *testing.T) {
	for height := 1; height <= 4; height++ {
		t.Run(fmt.Sprintf("20x%d", height), func(t *testing.T) {
			submitted := ""
			m := argEntryPaletteModel(&submitted)
			m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: height})
			view := stripAnsi(m.View())
			if strings.Contains(view, "Run /grep") && strings.Contains(view, "needle") {
				t.Fatalf("setup: command and argument unexpectedly readable:\n%s", view)
			}
			if !strings.Contains(view, "Resize") || !strings.Contains(view, "Esc Back") {
				t.Fatalf("hidden arg target lost status-bar recovery:\n%s", view)
			}
			if footer := stripAnsi(m.commandPalette.View(20, height)); strings.Contains(footer, "↵") {
				t.Fatalf("hidden arg target advertised Enter:\n%s", footer)
			}
			assertPaletteResizeHints(t, m, "Back")

			_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
			if submitted != "" || m.state != StateCommandPalette || !m.commandPalette.InArgEntry() {
				t.Fatalf("hidden arg entry submitted=%q state=%v arg-entry=%v", submitted, m.state, m.commandPalette.InArgEntry())
			}
			if m.commandPalette.argError != paletteArgumentResizeError {
				t.Fatalf("hidden arg entry feedback=%q", m.commandPalette.argError)
			}

			_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEscape})
			if m.state != StateCommandPalette || m.commandPalette.InArgEntry() {
				t.Fatalf("Esc did not recover to command list: state=%v arg-entry=%v", m.state, m.commandPalette.InArgEntry())
			}
		})
	}
}

func TestCommandPaletteArgEntryBlocksUnreadablyNarrowCommand(t *testing.T) {
	for width := 1; width < compactPaletteArgTargetMinWidth; width++ {
		submitted := ""
		m := argEntryPaletteModel(&submitted)
		m.applyResize(&tea.WindowSizeMsg{Width: width, Height: 12})
		_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if submitted != "" || !m.commandPalette.InArgEntry() {
			t.Errorf("%dx12 unreadable arg target submitted=%q arg-entry=%v", width, submitted, m.commandPalette.InArgEntry())
		}
	}
}

func TestCommandPaletteArgEntryReadableBoundaryStillSubmits(t *testing.T) {
	submitted := ""
	m := argEntryPaletteModel(&submitted)
	m.applyResize(&tea.WindowSizeMsg{Width: 20, Height: 5})
	view := stripAnsi(m.View())
	for _, want := range []string{"Run /grep", "needle"} {
		if !strings.Contains(view, want) {
			t.Fatalf("readable arg boundary lost %q:\n%s", want, view)
		}
	}

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/grep needle" || m.state != StateProcessing {
		t.Fatalf("readable arg entry submitted=%q state=%v", submitted, m.state)
	}
}

func TestCommandPaletteZeroSizeSentinelPreservesPreResizeExecution(t *testing.T) {
	runs := 0
	m := hiddenActionPaletteModel(&runs)

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})

	if runs != 1 || m.state != StateInput {
		t.Fatalf("unspecified palette geometry was incorrectly gated: runs=%d state=%v", runs, m.state)
	}
}

func TestCommandPaletteArgEntryZeroSizeSentinelPreservesPreResizeSubmission(t *testing.T) {
	submitted := ""
	m := argEntryPaletteModel(&submitted)

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/grep needle" || m.state != StateProcessing {
		t.Fatalf("unspecified arg-entry geometry was incorrectly gated: submitted=%q state=%v", submitted, m.state)
	}
}
