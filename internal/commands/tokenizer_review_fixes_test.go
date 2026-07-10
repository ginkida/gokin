package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --- Review fixes for the quote-aware parsing round (v0.100.77). ---
// The review of the tokenizer change caught two silent-corruption regressions
// vs the old strings.Fields behavior; each test here reproduces the exact
// reported scenario.

// TestSplitCommandFields_MidWordApostropheIsLiteral: an apostrophe INSIDE a
// word must not open quote mode — `/grep don't` used to search for "dont" and
// `/loop fix Bob's PR --max-tokens 200k` merged everything after the
// apostrophe into one arg, silently swallowing the --max-tokens flag (the
// token budget never applied on a quota-limited key).
func TestSplitCommandFields_MidWordApostropheIsLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"don't panic", []string{"don't", "panic"}},
		{"fix Bob's PR --max-tokens 200k", []string{"fix", "Bob's", "PR", "--max-tokens", "200k"}},
		// Token-start quoting still works (the feature the tokenizer shipped for).
		{`x "multi word desc" tools`, []string{"x", "multi word desc", "tools"}},
		{`'single quoted arg' tail`, []string{"single quoted arg", "tail"}},
		// Mid-word double quote is literal too (matches old strings.Fields).
		{`--desc="a b"`, []string{`--desc="a`, `b"`}},
	}
	for _, tc := range cases {
		if got := splitCommandFields(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitCommandFields(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFileCommands_UppercaseFilenameReachable: Parse lowercases the typed
// command name (case-insensitive slash commands), so file commands MUST
// register lowercase — a Deploy.md used to become unreachable under both
// /Deploy and /deploy (the raw "Deploy" map key never matched), silently
// sending the line to the model as chat.
func TestFileCommands_UppercaseFilenameReachable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Deploy.md"), []byte("run the deploy for $ARGUMENTS"), 0644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler()
	h.LoadFileCommands(dir, "")

	name, args, ok := h.Parse("/Deploy prod")
	if !ok {
		t.Fatal("/Deploy (uppercase file command) must parse as a command")
	}
	if name != "deploy" {
		t.Fatalf("expected canonical name 'deploy', got %q", name)
	}
	if len(args) != 1 || args[0] != "prod" {
		t.Fatalf("args = %q, want [prod]", args)
	}
	if _, exists := h.commands["deploy"]; !exists {
		t.Fatal("file command must be registered under its lowercase name")
	}
}
