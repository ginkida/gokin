package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func TestFirstLaunchTinyWelcomeLabelsActionsAndSafetyModes(t *testing.T) {
	tests := []struct {
		name          string
		permissions   bool
		sandbox       bool
		planning      bool
		modeFragments []string
	}{
		{name: "normal", permissions: true, sandbox: true, modeFragments: []string{"NORMAL", "asks"}},
		{name: "plan", permissions: true, sandbox: true, planning: true, modeFragments: []string{"PLAN", "read-only"}},
		{name: "prompts off", sandbox: true, modeFragments: []string{"YOLO", "SB on"}},
		{name: "sandbox off", permissions: true, modeFragments: []string{"!SB", "prompts on"}},
		{name: "yolo", modeFragments: []string{"YOLO", "SB off"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 20, 8
			m.permissionsEnabled = tt.permissions
			m.sandboxEnabled = tt.sandbox
			m.planningModeEnabled = tt.planning
			m.firstLaunchWelcomePending = true

			plain := ansi.Strip(m.renderWelcomePanel())
			for _, want := range append([]string{"Gokin", "Ctrl+P actions", "? shortcuts"}, tt.modeFragments...) {
				if !strings.Contains(plain, want) {
					t.Fatalf("tiny first-launch %s lost %q:\n%s", tt.name, want, plain)
				}
			}
			if got := renderedLineCount(plain); got > welcomeLineBudget(m.height) {
				t.Fatalf("tiny first-launch rows=%d exceed budget=%d:\n%s", got, welcomeLineBudget(m.height), plain)
			}
		})
	}

	// At the two-row floor safety and both discovery keys outrank branding.
	minimal := NewModel()
	minimal.width, minimal.height = 10, 6
	minimal.permissionsEnabled = false
	minimal.sandboxEnabled = false
	minimal.firstLaunchWelcomePending = true
	plain := ansi.Strip(minimal.renderWelcomePanel())
	for _, want := range []string{"YOLO", "Ctrl+P", "?"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("minimal unsafe welcome lost %q:\n%s", want, plain)
		}
	}
}

func TestWelcomeResizeKeepsUniqueTruthfulDiscoveryHints(t *testing.T) {
	m := NewModel()
	m.permissionsEnabled = true
	m.sandboxEnabled = true
	m.firstLaunchWelcomePending = true

	for _, size := range []struct {
		width, height int
		want          []string
	}{
		{width: 20, height: 8, want: []string{"Ctrl+P actions", "? shortcuts (empty)", "NORMAL"}},
		{width: 40, height: 12, want: []string{"Ctrl+P", "? shortcuts (empty)", "Shift+Tab cycles modes", "/quickstart", "NORMAL"}},
		{width: 60, height: 22, want: []string{"Ctrl+P", "Ctrl+S", "Ctrl+K", "? shortcuts (empty)", "Shift+Tab", "Normal/Plan/YOLO", "/quickstart", "normal mode"}},
		{width: 100, height: 24, want: []string{"Ctrl+P", "Ctrl+S", "Ctrl+K", "? shortcuts (empty)", "Shift+Tab", "Normal/Plan/YOLO", "/quickstart", "normal mode"}},
	} {
		m.width, m.height = size.width, size.height
		view := m.renderWelcomePanel()
		plain := ansi.Strip(view)
		for _, want := range size.want {
			if !strings.Contains(plain, want) {
				t.Fatalf("%dx%d welcome lost %q after resize:\n%s", size.width, size.height, want, plain)
			}
		}
		if count := strings.Count(plain, "Ctrl+P"); count != 1 {
			t.Fatalf("%dx%d welcome duplicated Ctrl+P hint %d times:\n%s", size.width, size.height, count, plain)
		}
		if rows := renderedLineCount(view); rows > welcomeLineBudget(size.height) {
			t.Fatalf("%dx%d rows=%d exceed budget=%d:\n%s", size.width, size.height, rows, welcomeLineBudget(size.height), plain)
		}
		for _, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d line width=%d:\n%s", size.width, size.height, got, plain)
			}
		}
	}
}

func TestWelcomeQuestionMarkHintNamesEmptyInputPrecondition(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.permissionsEnabled = true
	m.sandboxEnabled = true
	m.input.textarea.SetValue("draft")

	plain := ansi.Strip(m.renderWelcomePanel())
	if !strings.Contains(plain, "? shortcuts (empty)") {
		t.Fatalf("welcome advertised ? without its live empty-input precondition:\n%s", plain)
	}
}

func TestDegenerateWelcomeRendererNeverExceedsAnnouncedGeometry(t *testing.T) {
	for width := 1; width <= 10; width++ {
		for height := 1; height <= 5; height++ {
			m := NewModel()
			m.width, m.height = width, height
			m.firstLaunchWelcomePending = true
			view := m.renderWelcomePanel()
			if rows := renderedLineCount(view); rows > height {
				t.Fatalf("%dx%d welcome rows=%d exceed terminal:\n%s", width, height, rows, ansi.Strip(view))
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("%dx%d welcome line width=%d:\n%s", width, height, got, ansi.Strip(view))
				}
			}
		}
	}
}

func TestRecurringNormalWelcomeStaysCalmButFirstLaunchExplainsSafety(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.permissionsEnabled = true
	m.sandboxEnabled = true
	if plain := ansi.Strip(m.renderWelcomePanel()); strings.Contains(plain, "normal mode") || strings.Contains(plain, "NORMAL") {
		t.Fatalf("recurring protected welcome gained redundant mode chrome:\n%s", plain)
	}

	m.firstLaunchWelcomePending = true
	plain := ansi.Strip(m.renderWelcomePanel())
	for _, want := range []string{"normal mode", "asks before write/edit/bash"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("first launch did not explain protected mode %q:\n%s", want, plain)
		}
	}
}

func TestShortcutOverlayUsesTerminalNamesAndStableOnboardingMeanings(t *testing.T) {
	var rows []string
	for _, category := range DefaultShortcuts() {
		for _, shortcut := range category.Shortcuts {
			rows = append(rows, strings.Join(shortcut.Keys, "+")+" "+shortcut.Description)
		}
	}
	joined := strings.Join(rows, "\n")
	for _, want := range []string{
		"Alt+C Copy last AI response",
		"Ctrl+S Open settings",
		"Ctrl+T Toggle task list panel",
		"Ctrl+O Live activity detail on/off",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("shortcut onboarding lost %q:\n%s", want, joined)
		}
	}
	for _, stale := range []string{"Option+C", "todo list"} {
		if strings.Contains(joined, stale) {
			t.Fatalf("shortcut onboarding retained conflicting wording %q:\n%s", stale, joined)
		}
	}
}
