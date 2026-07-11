package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCommandFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestExpandFileCommandTemplate(t *testing.T) {
	cases := []struct {
		template string
		args     []string
		want     string
	}{
		{"Review $ARGUMENTS carefully", []string{"the", "auth", "flow"}, "Review the auth flow carefully"},
		{"Fix $1 in $2", []string{"the bug", "auth.go"}, "Fix the bug in auth.go"},
		{"Missing args: [$1][$3]", []string{"x"}, "Missing args: [x][]"},
		{"No placeholders", []string{"ignored"}, "No placeholders"},
		{"$ARGUMENTS", nil, ""},
	}
	for _, tc := range cases {
		if got := expandFileCommandTemplate(tc.template, tc.args); got != tc.want {
			t.Errorf("expand(%q, %v) = %q, want %q", tc.template, tc.args, got, tc.want)
		}
	}
}

func TestLoadFileCommands_EndToEnd(t *testing.T) {
	project := filepath.Join(t.TempDir(), "commands")
	writeCommandFile(t, project, "review-pr.md", `---
description: Review the current PR
argument-hint: "[focus]"
---
Review the current pull request. Focus: $ARGUMENTS.
Run the narrowest verification.`)

	h := NewHandler()
	loaded, warnings := h.LoadFileCommands(project, "")
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(loaded) != 1 || loaded[0].Name() != "review-pr" {
		t.Fatalf("loaded = %v", loaded)
	}

	cmd, ok := h.GetCommand("review-pr")
	if !ok {
		t.Fatal("file command must be reachable through the handler")
	}
	if !strings.Contains(cmd.Description(), "Review the current PR") || !strings.Contains(cmd.Description(), "project command") {
		t.Fatalf("description = %q", cmd.Description())
	}

	out, err := cmd.Execute(context.Background(), []string{"security"}, &fakeAppForMCP{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(out, PromptMarker) {
		t.Fatalf("file command must return the __prompt: marker, got %q", out)
	}
	if !strings.Contains(out, "Focus: security.") {
		t.Fatalf("argument expansion missing: %q", out)
	}
}

func TestLoadFileCommands_ConflictRules(t *testing.T) {
	project := filepath.Join(t.TempDir(), "p")
	global := filepath.Join(t.TempDir(), "g")
	// Shadowing a builtin — must be skipped with a warning.
	writeCommandFile(t, project, "help.md", "Body that tries to shadow /help")
	// Same name in both — project wins.
	writeCommandFile(t, project, "deploy.md", "project deploy: $ARGUMENTS")
	writeCommandFile(t, global, "deploy.md", "global deploy: $ARGUMENTS")
	// Global-only — loads fine. "sweep" (not "greet" — chosen not to collide
	// with any builtin, unlike "audit" which became a real builtin command).
	writeCommandFile(t, global, "sweep.md", "global sweep body")

	h := NewHandler()
	loaded, warnings := h.LoadFileCommands(project, global)

	names := map[string]string{}
	for _, c := range loaded {
		names[c.Name()] = c.Source()
	}
	if names["deploy"] != "project" {
		t.Fatalf("project must win the deploy conflict: %v", names)
	}
	if names["sweep"] != "global" {
		t.Fatalf("global-only command must load: %v", names)
	}
	if _, ok := names["help"]; ok {
		t.Fatal("builtin shadow must NOT load")
	}

	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "shadows a built-in") || !strings.Contains(joined, "shadowed by the project command") {
		t.Fatalf("warnings must explain both conflicts:\n%s", joined)
	}

	// The builtin is untouched.
	cmd, _ := h.GetCommand("help")
	if _, isFile := cmd.(*FileCommand); isFile {
		t.Fatal("/help must still be the builtin")
	}
}

func TestLoadFileCommands_PanicsAfterBootSeal(t *testing.T) {
	h := NewHandler()
	h.SealBootPhase() // simulate builder finishing the boot phase
	dir := filepath.Join(t.TempDir(), "commands")
	writeCommandFile(t, dir, "x.md", "body")

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("LoadFileCommands must panic when called after SealBootPhase")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "LoadFileCommands called after boot phase sealed") {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()
	_, _ = h.LoadFileCommands(dir, "")
}

func TestParseFileCommand_Validation(t *testing.T) {
	dir := t.TempDir()

	writeCommandFile(t, dir, "broken-fm.md", "---\ndescription: [unclosed\n---\nbody")
	if _, err := parseFileCommand(filepath.Join(dir, "broken-fm.md"), "project"); err == nil {
		t.Fatal("broken frontmatter must fail loudly")
	}

	writeCommandFile(t, dir, "empty.md", "---\ndescription: x\n---\n   \n")
	if _, err := parseFileCommand(filepath.Join(dir, "empty.md"), "project"); err == nil {
		t.Fatal("empty body must fail loudly")
	}

	writeCommandFile(t, dir, "unterminated.md", "---\ndescription: x\nbody without closing fence")
	if _, err := parseFileCommand(filepath.Join(dir, "unterminated.md"), "project"); err == nil {
		t.Fatal("unterminated frontmatter must fail loudly")
	}

	writeCommandFile(t, dir, "no-fm.md", "Plain body without frontmatter: $1")
	cmd, err := parseFileCommand(filepath.Join(dir, "no-fm.md"), "global")
	if err != nil {
		t.Fatalf("frontmatter must be optional: %v", err)
	}
	if cmd.Name() != "no-fm" || !strings.Contains(cmd.Description(), "Custom global command") {
		t.Fatalf("defaults wrong: %q / %q", cmd.Name(), cmd.Description())
	}
}
