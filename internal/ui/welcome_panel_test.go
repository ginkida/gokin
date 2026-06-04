package ui

import (
	"strings"
	"testing"
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
		"Ctrl+P",      // command palette shortcut (primary discovery)
		"all actions", // tip mentioning the palette
		"Ctrl+K",      // model selector shortcut
		"shortcuts",   // tip mentioning ?
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
