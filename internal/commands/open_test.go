package commands

import (
	"slices"
	"testing"
)

// TestParseEditorCommand pins the argv-style splitting that keeps /open
// safe from shell-injection. Any regression that reintroduces shell
// interpretation (e.g. by reverting to sh -c) would have to bypass this
// helper — catch it at the unit level.
func TestParseEditorCommand(t *testing.T) {
	cases := []struct {
		name     string
		editor   string
		wantCmd  string
		wantArgs []string
		wantOK   bool
	}{
		// Common legitimate patterns — must split correctly.
		{"bare vi", "vi", "vi", []string{}, true},
		{"vim", "vim", "vim", []string{}, true},
		{"macos open -t", "open -t", "open", []string{"-t"}, true},
		{"vscode with wait", "code --wait", "code", []string{"--wait"}, true},
		{"vscode multi-flag", "code --wait --goto", "code", []string{"--wait", "--goto"}, true},
		{"emacs terminal mode", "emacs -nw", "emacs", []string{"-nw"}, true},

		// Whitespace handling — shouldn't trip us up.
		{"leading spaces", "   vi", "vi", []string{}, true},
		{"trailing spaces", "vi   ", "vi", []string{}, true},
		{"tab separator", "code\t--wait", "code", []string{"--wait"}, true},
		{"mixed whitespace", "  code  --wait  --goto  ", "code", []string{"--wait", "--goto"}, true},

		// Empty / whitespace-only — reject so caller can surface a message.
		{"empty string", "", "", nil, false},
		{"spaces only", "   ", "", nil, false},
		{"tabs only", "\t\t", "", nil, false},

		// Pathological $EDITOR values: shell metacharacters must become
		// literal argv tokens, not shell syntax. The resulting executable
		// name won't exist and exec.Command will fail with a benign error
		// rather than executing arbitrary code.
		{"semicolon injection", "vi; rm -rf ~", "vi;", []string{"rm", "-rf", "~"}, true},
		{"pipe injection", "vi|nc attacker", "vi|nc", []string{"attacker"}, true},
		{"backtick injection", "`rm -rf ~`", "`rm", []string{"-rf", "~`"}, true},
		{"dollar injection", "$(rm -rf)", "$(rm", []string{"-rf)"}, true},
		{"newline", "vi\nrm -rf ~", "vi", []string{"rm", "-rf", "~"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCmd, gotArgs, gotOK := parseEditorCommand(tc.editor)
			if gotCmd != tc.wantCmd {
				t.Errorf("cmd = %q, want %q", gotCmd, tc.wantCmd)
			}
			if gotOK != tc.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if tc.wantOK {
				// Normalize nil vs []string{} for comparison. Both mean "no args".
				want := tc.wantArgs
				if want == nil {
					want = []string{}
				}
				got := gotArgs
				if got == nil {
					got = []string{}
				}
				if !slices.Equal(got, want) {
					t.Errorf("args = %v, want %v", got, want)
				}
			}
		})
	}
}
