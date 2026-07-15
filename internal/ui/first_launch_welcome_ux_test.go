package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestFirstLaunchWelcomeWaitsForGeometryAndPreservesFirstKey(t *testing.T) {
	for _, size := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "tiny", width: 20, height: 8},
		{name: "medium", width: 40, height: 12},
	} {
		t.Run(size.name, func(t *testing.T) {
			m := NewModel()
			m.AddSystemMessage("STARTUP NOTICE")

			callbackCalls := 0
			m.ShowFirstLaunchWelcome(func() {
				callbackCalls++
			})

			// Arming the first-launch surface must not persist it as seen. In
			// particular, View's pre-resize fallback geometry is not proof that
			// the terminal ever displayed a usable frame.
			_ = m.View()
			if callbackCalls != 0 {
				t.Fatalf("welcome callback ran before terminal geometry arrived: got %d calls", callbackCalls)
			}

			resized, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			model := resized.(Model)
			welcome := ansi.Strip(model.View())
			for _, essential := range []string{"Gokin", "Ctrl+P", "?"} {
				if !strings.Contains(welcome, essential) {
					t.Fatalf("%dx%d first-launch surface lost %q:\n%s", size.width, size.height, essential, welcome)
				}
			}
			if strings.Contains(welcome, "STARTUP NOTICE") {
				t.Fatalf("%dx%d startup output leaked through first-launch surface:\n%s", size.width, size.height, welcome)
			}
			if callbackCalls != 0 {
				t.Fatalf("welcome callback ran merely because the surface rendered: got %d calls", callbackCalls)
			}

			updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			afterFirstKey := updated.(Model)
			if callbackCalls != 1 {
				t.Fatalf("first printable key invoked welcome callback %d times, want 1", callbackCalls)
			}
			if got := afterFirstKey.input.Value(); got != "x" {
				t.Fatalf("first printable key was swallowed while dismissing welcome: input=%q, want %q", got, "x")
			}
			dismissed := ansi.Strip(afterFirstKey.View())
			if !strings.Contains(strings.Join(strings.Fields(dismissed), " "), "STARTUP NOTICE") {
				t.Fatalf("startup output did not become visible after welcome dismissal:\n%s", dismissed)
			}

			updated, _ = afterFirstKey.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
			afterSecondKey := updated.(Model)
			if callbackCalls != 1 {
				t.Fatalf("later key re-invoked welcome callback: got %d calls, want 1", callbackCalls)
			}
			if got := afterSecondKey.input.Value(); got != "xz" {
				t.Fatalf("input stopped receiving keys after welcome dismissal: got %q, want %q", got, "xz")
			}
		})
	}
}

func TestMediumWelcomePrioritizesBranchOverLongProjectMetadata(t *testing.T) {
	m := NewModel()
	m.workDir = "/a/very/long/project/path/whose/end-cannot-fit-in-the-terminal"
	m.projectName = "an equally long project name"
	m.gitBranch = "main"
	m.applyResize(&tea.WindowSizeMsg{Width: 40, Height: 12})

	plain := ansi.Strip(m.renderWelcomePanel())
	if !strings.Contains(plain, "branch: main") {
		t.Fatalf("medium welcome hid the actionable branch behind long project metadata:\n%s", plain)
	}
}
