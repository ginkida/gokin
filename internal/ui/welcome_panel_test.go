package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// TestWelcomePanel_ContainsExpectedSections pins the structural contract
// of the welcome screen. Substring-based (not byte-identical golden) so
// styling changes (lipgloss color codes) don't break the test, but the
// shape of the panel — wordmark, version, tips header, key hints —
// stays stable across casual refactors.
//
// If someone removes the tips section or renames the section header,
// or drops a key hint, this trips.
func TestWelcomePanel_ContainsExpectedSections(t *testing.T) {
	m := Model{
		version: "0.83.0",
		width:   100,
	}
	got := m.renderWelcomePanel()

	wantSubstrings := []string{
		"┌─┐",            // wordmark top edge (g+o+k uses these box-drawings)
		"│ ┬",            // wordmark middle (g)
		"└─┘",            // wordmark bottom (g+o)
		"v0.83.0",        // version line
		"tips",           // section header
		"slash commands", // tip mentioning /
		"to pin a file",  // tip mentioning @
		"Ctrl+P",         // command palette shortcut (primary discovery)
		"all actions",    // tip mentioning the palette
		"Ctrl+K",         // model selector shortcut
		"shortcuts",      // tip mentioning ?
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("welcome panel missing expected substring %q\n--- output ---\n%s\n--- end ---", want, got)
		}
	}
}

// TestWelcomePanel_ProjectSectionHiddenWhenEmpty pins the omit-when-empty
// behavior of welcomeProjectSection — a fresh shell with no project
// context should NOT show an empty "project" header.
func TestWelcomePanel_ProjectSectionHiddenWhenEmpty(t *testing.T) {
	m := Model{
		version: "0.83.0",
		width:   100,
		// No workDir, projectName, or gitBranch set.
	}
	got := m.renderWelcomePanel()
	if strings.Contains(got, "project") && !strings.Contains(got, "branch:") {
		// If "project" appears without "branch:" we may have an orphan
		// header. Allow for the slash command hint "for slash commands"
		// being adjacent — check more strictly.
		// Stronger check: confirm "project\n" (the section header) is absent.
		// section header has 2 leading spaces.
		if strings.Contains(got, "  project") {
			t.Errorf("welcome panel rendered empty `project` section, got:\n%s", got)
		}
	}
}

// TestWelcomePanel_VersionPrefixed pins the v-prefix idempotency on the
// version string. If a user passes "v0.83.0" it shouldn't become "vv0.83.0";
// if they pass "0.83.0" it should display as "v0.83.0".
func TestWelcomePanel_VersionPrefixed(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"0.83.0", "v0.83.0"},
		{"v0.83.0", "v0.83.0"},
		{"V0.83.0", "V0.83.0"}, // capital V also passes through
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			m := Model{version: tc.input, width: 100}
			got := m.renderWelcomePanel()
			if !strings.Contains(got, tc.want) {
				t.Errorf("version %q should render as %q, got:\n%s", tc.input, tc.want, got)
			}
			// Double-v sanity check — never want "vv".
			if strings.Contains(got, "vv") {
				t.Errorf("version %q produced double-v prefix:\n%s", tc.input, got)
			}
		})
	}
}

func TestWelcomePanel_AdaptsToTerminalGeometry(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 10, height: 6},
		{width: 20, height: 8},
		{width: 40, height: 12},
		{width: 80, height: 18},
		{width: 100, height: 24},
	} {
		m := Model{
			width:       size.width,
			height:      size.height,
			version:     "\x1b[31m0.99\nFORGED VERSION",
			workDir:     "/very/long/project/path/with/界面/that/does/not/fit\nFORGED PATH",
			projectName: "a project name that is deliberately much too long",
			gitBranch:   "feature/a-very-long-branch\x1b[2J\nFORGED BRANCH",
		}
		got := m.renderWelcomePanel()
		for _, line := range strings.Split(got, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > size.width {
				t.Fatalf("%dx%d welcome line width=%d, want <=%d: %q", size.width, size.height, lineWidth, size.width, ansi.Strip(line))
			}
		}
		if rows := renderedLineCount(got); rows > max(size.height-4, 2) {
			t.Fatalf("%dx%d welcome rows=%d, want <=%d\n%s", size.width, size.height, rows, max(size.height-4, 2), ansi.Strip(got))
		}
		plain := ansi.Strip(got)
		for _, essential := range []string{"Ctrl+P", "?"} {
			if !strings.Contains(plain, essential) {
				t.Fatalf("%dx%d welcome lost essential %q:\n%s", size.width, size.height, essential, plain)
			}
		}
		if strings.Contains(plain, "\nFORGED") {
			t.Fatalf("%dx%d runtime metadata injected a visual row:\n%s", size.width, size.height, plain)
		}
	}
}

func TestWelcomePanel_ShowsProjectNameWithoutWorkingDirectory(t *testing.T) {
	m := Model{width: 80, height: 24, projectName: "payments-api"}
	plain := ansi.Strip(m.renderWelcomePanel())
	if !strings.Contains(plain, "payments-api") {
		t.Fatalf("project-only context disappeared from welcome panel:\n%s", plain)
	}
}

func TestPaletteCompactModeIsDiscoverableWithoutUnsafeShortcut(t *testing.T) {
	m := NewModel()
	m.RegisterPaletteActions()

	var compact *EnhancedPaletteCommand
	for i := range m.commandPalette.actionCommands {
		command := &m.commandPalette.actionCommands[i]
		if command.ActionID == paletteActionCompactMode {
			compact = command
			break
		}
	}
	if compact == nil {
		t.Fatal("compact mode action is missing from command palette")
	}
	if compact.Shortcut != "" {
		t.Fatalf("compact mode advertises unsafe terminal chord %q", compact.Shortcut)
	}
	if !strings.Contains(strings.ToLower(compact.Description), "palette") {
		t.Fatalf("compact mode does not explain how it is reached: %q", compact.Description)
	}
}
