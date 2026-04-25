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

// newLogTestApp builds a tempdir git repo with N seed commits so /log
// has something to render. Returns the workDir; the caller can chain
// further commits if a test needs specific topology.
func newLogTestApp(t *testing.T, seedCommits int) (*fakeAppForAuth, string) {
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

	// Each commit modifies a small file so they form a real chain.
	for i := 0; i < seedCommits; i++ {
		name := filepath.Join(dir, "f.txt")
		body := strings.Repeat("x", i+1) + "\n"
		if err := os.WriteFile(name, []byte(body), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		for _, c := range [][]string{
			{"git", "add", "f.txt"},
			{"git", "commit", "--quiet", "-m", "step " + strings.Repeat("X", i+1)},
		} {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("commit step %d (%v): %v: %s", i, c, err, out)
			}
		}
	}

	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = dir
	return app, dir
}

// TestLog_DefaultCount renders 3 seed commits with the default count
// of 10 — fewer than the limit, so all three appear with the
// expected fields (hash, relative time, author, subject).
func TestLog_DefaultCount(t *testing.T) {
	app, _ := newLogTestApp(t, 3)

	out, err := (&LogCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	must := []string{
		"Commits (last 10):", // header
		"step X",             // subject from seed
		"step XXX",           // newest subject
		"Test",               // author
	}
	for _, s := range must {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n  got: %s", s, out)
		}
	}
}

// TestLog_CountArg — explicit count clamps to logMaxCount and parses
// integers. `/log 9999` should not freeze the TUI by fetching the
// entire repo history.
func TestLog_CountArg(t *testing.T) {
	app, _ := newLogTestApp(t, 5)

	// Cap test: /log 9999 should produce a header that says "last 100"
	// (logMaxCount), not "last 9999" or anything goofy.
	out, err := (&LogCommand{}).Execute(context.Background(), []string{"9999"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "last 100") {
		t.Errorf("count cap failed (expected 'last 100' in header), got: %s", out)
	}

	// Smaller count: /log 2 should render exactly 2 commit lines.
	out, err = (&LogCommand{}).Execute(context.Background(), []string{"2"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// Count lines that look like a commit row (start with two spaces +
	// a hex hash + " · ").
	commitLines := 0
	for _, line := range strings.Split(out, "\n") {
		// pretty-format renders as "<hash> · <relative time> · <author> · <subject>"
		if strings.Count(line, " · ") >= 3 {
			commitLines++
		}
	}
	if commitLines != 2 {
		t.Errorf("expected 2 commit rows for /log 2, got %d\n  output: %s", commitLines, out)
	}
}

// TestLog_FilePathScopesCommits — passing a file path filters log to
// commits that touched that path.
func TestLog_FilePathScopesCommits(t *testing.T) {
	app, dir := newLogTestApp(t, 2)

	// Add a commit touching a DIFFERENT file so we can verify scoping.
	other := filepath.Join(dir, "other.txt")
	if err := os.WriteFile(other, []byte("hi\n"), 0644); err != nil {
		t.Fatalf("write other: %v", err)
	}
	for _, c := range [][]string{
		{"git", "add", "other.txt"},
		{"git", "commit", "--quiet", "-m", "untouched-by-f"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("untouched commit (%v): %v: %s", c, err, out)
		}
	}

	// Scoped to f.txt should NOT include the other.txt commit.
	out, err := (&LogCommand{}).Execute(context.Background(), []string{"f.txt"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "touching f.txt") {
		t.Errorf("expected scoped header, got: %s", out)
	}
	if strings.Contains(out, "untouched-by-f") {
		t.Errorf("scoped log must hide unrelated commits, got: %s", out)
	}
	if !strings.Contains(out, "step X") {
		t.Errorf("scoped log should still show f.txt commits, got: %s", out)
	}
}

// TestLog_CountAndFile — both args together.
func TestLog_CountAndFile(t *testing.T) {
	app, _ := newLogTestApp(t, 5)
	out, err := (&LogCommand{}).Execute(context.Background(), []string{"3", "f.txt"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "last 3 touching f.txt") {
		t.Errorf("expected combined scope label, got: %s", out)
	}
}

// TestLog_EmptyRepo — friendly message rather than git's terse exit.
func TestLog_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	for _, c := range [][]string{
		{"git", "init", "--quiet", "-b", "main"},
		{"git", "config", "user.email", "x@x"},
		{"git", "config", "user.name", "x"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v: %s", c, err, out)
		}
	}
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = dir

	out, err := (&LogCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "No commits") && !strings.Contains(out, "Failed to read git log") {
		t.Errorf("empty repo should produce a friendly message, got: %s", out)
	}
}

// TestLog_NotAGitRepo — graceful for non-git dirs.
func TestLog_NotAGitRepo(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = t.TempDir()

	out, err := (&LogCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Not a git repository") {
		t.Errorf("non-git dir should say so, got: %s", out)
	}
}
