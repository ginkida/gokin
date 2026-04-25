package ui

import (
	"strings"
	"testing"
)

// TestWelcomeModeBadge_PerMode pins the substring contract for the
// per-mode welcome banner. Stripping ANSI is brittle, so we just
// assert the human-readable label and (for plan + yolo) the short
// explanatory hint that helps first-time users understand the mode
// before they trigger it experimentally.
func TestWelcomeModeBadge_PerMode(t *testing.T) {
	cases := []struct {
		name             string
		planEnabled      bool
		permsEnabled     bool
		sandboxEnabled   bool
		wantSubstrings   []string
		notWantSubstring string // sanity check — modes shouldn't bleed into each other
	}{
		{
			name:             "normal_mode",
			planEnabled:      false,
			permsEnabled:     true,
			sandboxEnabled:   true,
			wantSubstrings:   []string{"normal mode", "asks before"},
			notWantSubstring: "plan mode",
		},
		{
			name:             "plan_mode",
			planEnabled:      true,
			permsEnabled:     true,
			sandboxEnabled:   true,
			wantSubstrings:   []string{"plan mode", "read-only", "proposes"},
			notWantSubstring: "YOLO",
		},
		{
			name:             "yolo_mode_perms_off",
			planEnabled:      false,
			permsEnabled:     false,
			sandboxEnabled:   true,
			wantSubstrings:   []string{"YOLO", "no prompts"},
			notWantSubstring: "normal mode",
		},
		{
			name:             "yolo_mode_sandbox_off",
			planEnabled:      false,
			permsEnabled:     true,
			sandboxEnabled:   false,
			wantSubstrings:   []string{"YOLO"},
			notWantSubstring: "normal mode",
		},
		// Plan-mode flag wins even if the user has perms/sandbox toggled
		// off — matches the priority order in App.currentSessionMode.
		{
			name:             "plan_overrides_yolo_signals",
			planEnabled:      true,
			permsEnabled:     false,
			sandboxEnabled:   false,
			wantSubstrings:   []string{"plan mode"},
			notWantSubstring: "YOLO",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{
				planningModeEnabled: tc.planEnabled,
				permissionsEnabled:  tc.permsEnabled,
				sandboxEnabled:      tc.sandboxEnabled,
			}
			got := m.welcomeModeBadge()
			for _, want := range tc.wantSubstrings {
				if !strings.Contains(got, want) {
					t.Errorf("badge for %s missing %q\n  got: %s", tc.name, want, got)
				}
			}
			if tc.notWantSubstring != "" && strings.Contains(got, tc.notWantSubstring) {
				t.Errorf("badge for %s should NOT contain %q\n  got: %s", tc.name, tc.notWantSubstring, got)
			}
		})
	}
}
