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

// newBranchesTestApp builds a tempdir git repo with the requested
// branches. The first branch is checked out as current. Each branch
// gets its own commit so /branches has hashes + subjects to render.
func newBranchesTestApp(t *testing.T, branches []string) (*fakeAppForAuth, string) {
	t.Helper()
	dir := t.TempDir()

	for _, c := range [][]string{
		{"git", "init", "--quiet", "-b", branches[0]},
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

	// Make a baseline commit on the initial branch.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	for _, c := range [][]string{
		{"git", "add", "f.txt"},
		{"git", "commit", "--quiet", "-m", "init " + branches[0]},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seed commit %v: %v: %s", c, err, out)
		}
	}

	// Create remaining branches with one commit each, then return to
	// the first branch as current.
	for _, name := range branches[1:] {
		for _, c := range [][]string{
			{"git", "checkout", "--quiet", "-b", name},
		} {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("branch %s: %v: %s", name, err, out)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("change on "+name+"\n"), 0644); err != nil {
			t.Fatalf("modify on %s: %v", name, err)
		}
		for _, c := range [][]string{
			{"git", "add", "f.txt"},
			{"git", "commit", "--quiet", "-m", "work on " + name},
		} {
			cmd := exec.Command(c[0], c[1:]...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("commit on %s: %v: %s", name, err, out)
			}
		}
	}
	// Return to the first (root) branch as current.
	cmd := exec.Command("git", "checkout", "--quiet", branches[0])
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("return to %s: %v: %s", branches[0], err, out)
	}

	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = dir
	return app, dir
}

// TestBranches_ListsAllWithCurrentMarker — basic case: 3 branches,
// /branches shows all three, marks the current one with "*".
func TestBranches_ListsAllWithCurrentMarker(t *testing.T) {
	app, _ := newBranchesTestApp(t, []string{"main", "feature-x", "fix-bug"})

	out, err := (&BranchesCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	for _, name := range []string{"main", "feature-x", "fix-bug"} {
		if !strings.Contains(out, name) {
			t.Errorf("branch list missing %q\n  got: %s", name, out)
		}
	}

	// "* main" should appear (current marker on main); "* feature-x"
	// should NOT (it's not current).
	if !strings.Contains(out, "* main") {
		t.Errorf("current-branch marker (* main) missing\n  got: %s", out)
	}
	if strings.Contains(out, "* feature-x") {
		t.Errorf("non-current branch should not have * marker, got: %s", out)
	}

	// Each row should include the relative time (renders as e.g.
	// "0 seconds ago" for our just-made commits).
	if !strings.Contains(out, "ago") {
		t.Errorf("expected relative time hints in output, got: %s", out)
	}
}

// TestBranches_DetachedHEAD — when HEAD is detached, no branch is
// current. The output should still list branches but include a
// "_HEAD is detached_" footer so the user understands why nothing is
// marked.
func TestBranches_DetachedHEAD(t *testing.T) {
	app, dir := newBranchesTestApp(t, []string{"main", "feature"})

	// Detach HEAD by checking out the HEAD commit directly.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	hashOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %v: %s", err, hashOut)
	}
	hash := strings.TrimSpace(string(hashOut))
	cmd = exec.Command("git", "checkout", "--quiet", hash)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("detach checkout: %v: %s", err, out)
	}

	out, err := (&BranchesCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "HEAD is detached") {
		t.Errorf("detached HEAD should be flagged in footer, got: %s", out)
	}
	// No branch should have a "*" marker.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "* ") {
			t.Errorf("no * marker expected on detached HEAD, got line: %q", line)
		}
	}
}

// TestBranches_NotAGitRepo — graceful for non-git dirs.
func TestBranches_NotAGitRepo(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = t.TempDir()

	out, err := (&BranchesCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Not a git repository") {
		t.Errorf("non-git dir should say so, got: %s", out)
	}
}

// TestBranches_AllFlagIncludesRemoteScope — `--all` switches the
// scope label and includes remote refs in the listing. We can't
// easily fake a remote in a unit test, but we can verify the scope
// label changes (proves the flag was parsed).
func TestBranches_AllFlagIncludesRemoteScope(t *testing.T) {
	app, _ := newBranchesTestApp(t, []string{"main"})

	out, err := (&BranchesCommand{}).Execute(context.Background(), []string{"--all"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "local + remotes") {
		t.Errorf("--all flag should switch scope label, got: %s", out)
	}
}
