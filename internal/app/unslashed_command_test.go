package app

import (
	"strings"
	"testing"

	"gokin/internal/commands"
)

// TestDetectUnslashedCommand verifies the "forgot the slash" heuristic that
// surfaces when a user types `provider kimi` (no slash) and it silently
// goes to the LLM instead of running as a command. Must fire for obvious
// typos without flagging genuine natural-language prompts.
func TestDetectUnslashedCommand(t *testing.T) {
	app := &App{
		commandHandler: commands.NewHandler(),
	}

	cases := []struct {
		name     string
		input    string
		wantHint bool
	}{
		// Should fire: obvious forgot-slash patterns the user reported.
		{"provider + arg", "provider kimi", true},
		{"login + provider + key", "login deepseek sk-abc", true},
		{"bare provider", "provider", true},
		{"bare clear", "clear", true},
		{"bare help", "help", true},
		{"model + name", "model deepseek-v4-pro", true},
		{"case insensitive first word", "Provider kimi", true},

		// Should NOT fire: genuine natural language.
		{"sentence with punctuation", "provider options, please.", false},
		{"question mark", "provider kimi?", false},
		{"long sentence", "I want to provider a new model for testing", false},
		{"contains colon", "provider: kimi", false},
		{"first word not a command", "explain this code", false},
		{"explicit command", "/provider kimi", false},
		{"empty", "", false},
		{"spaces only", "   ", false},
		{"first word unknown", "foo bar baz", false},

		// Edge cases.
		{"exactly 4 words command-like", "login provider key here", true},
		{"5 words rejects", "login provider key here extra", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := app.detectUnslashedCommand(tc.input)
			if tc.wantHint && got == "" {
				t.Errorf("expected a hint for %q, got empty string", tc.input)
			}
			if !tc.wantHint && got != "" {
				t.Errorf("expected no hint for %q, got %q", tc.input, got)
			}
			if tc.wantHint && got != "" {
				// Hint must mention the command name so user knows what to retype.
				firstWord := strings.ToLower(strings.Fields(tc.input)[0])
				if !strings.Contains(got, "/"+firstWord) {
					t.Errorf("hint should mention /%s, got: %q", firstWord, got)
				}
			}
		})
	}
}

// Nil-safe: if App is malformed (no commandHandler), must not panic.
func TestDetectUnslashedCommand_NilHandler(t *testing.T) {
	app := &App{}
	got := app.detectUnslashedCommand("provider kimi")
	if got != "" {
		t.Errorf("expected empty hint when commandHandler is nil, got %q", got)
	}
}
