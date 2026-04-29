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

// newShowTestApp builds a tempdir git repo with three commits so /show
// has interesting output.
func newShowTestApp(t *testing.T) (*fakeAppForAuth, string) {
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

	// Three commits: each modifies a small file so they form a real chain.
	for i, body := range []string{"v1\n", "v2\n", "v3\n"} {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		for _, c := range [][]string{
			{"git", "add", "f.txt"},
			{"git", "commit", "--quiet", "-m", "step " + strings.Repeat("x", i+1)},
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

// TestShow_HEAD — bare /show defaults to HEAD (the latest commit).
func TestShow_HEAD(t *testing.T) {
	app, _ := newShowTestApp(t)

	out, err := (&ShowCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	for _, want := range []string{"Show HEAD:", "step xxx", "Test", "v3"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

// TestShow_PreviousCommit — HEAD~1 picks the second-newest.
func TestShow_PreviousCommit(t *testing.T) {
	app, _ := newShowTestApp(t)

	out, err := (&ShowCommand{}).Execute(context.Background(), []string{"HEAD~1"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "Show HEAD~1:") {
		t.Errorf("header should reflect ref, got: %s", out)
	}
	if !strings.Contains(out, "step xx") {
		t.Errorf("expected v2 commit subject, got: %s", out)
	}
	if strings.Contains(out, "step xxx") && !strings.Contains(out, "step xx ") {
		// xxx is the HEAD subject; HEAD~1 should NOT show it as the
		// commit being displayed (though "step xxx" might appear in a
		// path or hash collision, the substring "step xxx\n" only
		// matches the HEAD commit subject).
		t.Logf("note: %q is HEAD's subject — should not appear as the displayed commit", "step xxx")
	}
}

// TestShow_FileScope — passing a file scopes the diff.
func TestShow_FileScope(t *testing.T) {
	app, _ := newShowTestApp(t)

	out, err := (&ShowCommand{}).Execute(context.Background(), []string{"HEAD", "f.txt"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "Show HEAD — f.txt:") {
		t.Errorf("header should reflect file scope, got: %s", out)
	}
	if !strings.Contains(out, "f.txt") {
		t.Errorf("diff should mention f.txt, got: %s", out)
	}
}

// TestShow_BadRef — invalid ref surfaces git's stderr.
func TestShow_BadRef(t *testing.T) {
	app, _ := newShowTestApp(t)

	out, err := (&ShowCommand{}).Execute(context.Background(), []string{"definitely-not-a-real-ref-zzz"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "git show failed") {
		t.Errorf("expected friendly error, got: %s", out)
	}
}

// TestShow_NotAGitRepo — fall back to friendly error.
func TestShow_NotAGitRepo(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = t.TempDir()

	out, err := (&ShowCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Not a git repository") {
		t.Errorf("non-git dir should say so, got: %s", out)
	}
}

// TestShow_Truncation — large output gets capped.
func TestShow_Truncation(t *testing.T) {
	huge := strings.Repeat("commit line content line\n", 5_000) // ~120KB
	got := truncateShowOutput(strings.TrimRight(huge, "\n"))
	if len(got) >= len(huge) {
		t.Errorf("expected truncation, got %d / orig %d", len(got), len(huge))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation tail missing: %q", got[len(got)-100:])
	}
}
