package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
)

// newGrepTestApp builds a real git tempdir with a couple of seed files
// so /grep has actual content to search. The repo is committed once so
// `git grep` (which scans the index by default for tracked files plus
// the working tree) has indexed entries to match against.
func newGrepTestApp(t *testing.T) (*fakeAppForAuth, string) {
	t.Helper()
	dir := t.TempDir()

	for _, c := range [][]string{
		{"git", "init", "--quiet", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v: %s", c, err, out)
		}
	}

	// Seed two files in different subdirs so we can also test path
	// scoping. Mixed-case content lets us verify case-insensitive
	// default behavior.
	if err := os.MkdirAll(filepath.Join(dir, "internal"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"hello.txt":         "Hello world\nTODO: refactor\nmoretodo here\n",
		"internal/main.go":  "package main\n\n// TODO: implement\nfunc main() {}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	for _, c := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "--quiet", "-m", "seed"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seed commit %v: %v: %s", c, err, out)
		}
	}

	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = dir
	return app, dir
}

// TestGrep_BasicMatch — pattern hits multiple files, output names
// each file with line number.
func TestGrep_BasicMatch(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"TODO"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	for _, want := range []string{"hello.txt", "internal/main.go", "TODO"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
	// Header should announce the pattern + match count.
	if !strings.Contains(out, `Matches for "TODO"`) {
		t.Errorf("header missing pattern, got: %s", out)
	}
}

// TestGrep_CaseInsensitiveByDefault — bare /grep is case-insensitive
// so "hello" finds "Hello".
func TestGrep_CaseInsensitiveByDefault(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"hello"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Hello world") {
		t.Errorf("default case-insensitive match should find 'Hello world', got: %s", out)
	}
}

// TestGrep_WholeWord — `-w` rejects substring matches. "todo" alone
// hits TODO (case-insensitive default), but "todo" with `-w` should
// NOT match "moretodo".
func TestGrep_WholeWord(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"-w", "todo"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Should still match "TODO:" (whole word, colon is non-word).
	if !strings.Contains(out, "TODO") {
		t.Errorf("whole-word should still match TODO, got: %s", out)
	}
	// Should NOT match "moretodo" (todo is a substring there).
	if strings.Contains(out, "moretodo") {
		t.Errorf("whole-word should reject substring 'moretodo', got: %s", out)
	}
}

// TestGrep_NoMatch — pattern absent from tree. Returns friendly
// no-match notice, not an error.
func TestGrep_NoMatch(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"zzzNonexistent"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "No matches") {
		t.Errorf("expected friendly no-match copy, got: %s", out)
	}
}

// TestGrep_PathScope — restricting to a path filters matches to that
// subtree only.
func TestGrep_PathScope(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"TODO", "internal/"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "internal/main.go") {
		t.Errorf("path-scoped match should include internal/main.go, got: %s", out)
	}
	if strings.Contains(out, "hello.txt") {
		t.Errorf("path scope should exclude top-level hello.txt, got: %s", out)
	}
	// Header should reflect the scope.
	if !strings.Contains(out, "internal/") {
		t.Errorf("header should mention path scope, got: %s", out)
	}
}

// TestGrep_ContextLines — `-C 1` adds one line of context above and
// below each match.
func TestGrep_ContextLines(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"-C", "1", "implement"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Context should pull in surrounding lines from main.go.
	// "// TODO: implement" is the match; "package main" or
	// "func main()" sits adjacent.
	if !strings.Contains(out, "implement") {
		t.Errorf("expected match line in output, got: %s", out)
	}
	if !strings.Contains(out, "func main()") && !strings.Contains(out, "package main") {
		t.Errorf("expected adjacent context line in output, got: %s", out)
	}
}

// TestGrep_NoArgs — empty args print usage hint.
func TestGrep_NoArgs(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Usage:") {
		t.Errorf("no-arg invocation should print usage, got: %s", out)
	}
}

// TestGrep_NotAGitRepo — fall back to a friendly error rather than
// crashing in a non-git working directory.
func TestGrep_NotAGitRepo(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = t.TempDir()

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"foo"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Not a git repository") {
		t.Errorf("non-git dir should say so, got: %s", out)
	}
}

// TestGrep_TruncationGuard — too-broad output is capped with a
// helpful tail rather than flooding the scrollback.
func TestGrep_TruncationGuard(t *testing.T) {
	huge := strings.Repeat("file.txt:1:line content\n", 5_000) // ~115KB
	got := truncateGrepOutput(huge)
	if len(got) >= len(huge) {
		t.Errorf("expected truncation, got %d / orig %d", len(got), len(huge))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation tail missing, got tail: %q", got[len(got)-100:])
	}
}

// TestGrep_InvalidContextValue — bogus -C value rejected with usage
// hint, not propagated to git grep.
func TestGrep_InvalidContextValue(t *testing.T) {
	app, _ := newGrepTestApp(t)

	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"-C", "abc", "TODO"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Invalid -C value") {
		t.Errorf("non-numeric -C should be rejected, got: %s", out)
	}
}

// TestGrep_SurfacesStderrOnFailure — when git grep fails for a non-empty
// reason (bad pathspec, malformed regex), v0.78.20 surfaces git's actual
// stderr instead of a bare "exit status N". Pre-fix the user got no clue
// what went wrong.
func TestGrep_SurfacesStderrOnFailure(t *testing.T) {
	app, _ := newGrepTestApp(t)

	// Pass a path that doesn't exist — `git grep -- pattern non-existent-path`
	// fails with "fatal: pathspec '<path>' did not match any file(s) known
	// to git" on the stderr stream.
	out, err := (&GrepCommand{}).Execute(context.Background(), []string{"TODO", "no-such-dir-zzz"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// Must surface something git-flavored: "fatal" / "pathspec" / "did not
	// match" — not just a bare exit code. Different git versions phrase
	// this slightly differently, so accept any keyword.
	hasSignal := false
	for _, want := range []string{"fatal", "pathspec", "did not match", "no-such-dir-zzz"} {
		if strings.Contains(out, want) {
			hasSignal = true
			break
		}
	}
	if !hasSignal {
		t.Errorf("error message should surface git's stderr; got bare: %s", out)
	}
}
