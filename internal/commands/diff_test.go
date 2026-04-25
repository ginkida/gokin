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

// newDiffTestApp builds a minimal AppInterface backed by a real git
// repository in a tempdir so /diff can run against actual `git diff`
// output. Returns the workDir for direct file mutation in tests.
func newDiffTestApp(t *testing.T) (*fakeAppForAuth, string) {
	t.Helper()
	dir := t.TempDir()

	// Initialize a git repo with a baseline commit so subsequent
	// modifications produce a non-trivial diff.
	cmds := [][]string{
		{"git", "init", "--quiet", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "commit.gpgsign", "false"},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v: %s", c, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("baseline\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, c := range [][]string{
		{"git", "add", "hello.txt"},
		{"git", "commit", "--quiet", "-m", "init"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v: %s", c, err, out)
		}
	}

	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = dir
	return app, dir
}

// TestDiff_CleanTreeReportsNothing verifies the empty-state copy is
// human-friendly rather than the silent empty result git would
// produce.
func TestDiff_CleanTreeReportsNothing(t *testing.T) {
	app, _ := newDiffTestApp(t)

	out, err := (&DiffCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Working tree is clean") {
		t.Errorf("clean tree should produce friendly notice, got: %q", out)
	}
}

// TestDiff_ShowsUnstagedChanges — the basic case: user edits a file,
// runs /diff, sees the diff with both stat header and content body.
func TestDiff_ShowsUnstagedChanges(t *testing.T) {
	app, dir := newDiffTestApp(t)

	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("baseline\nchanged line\n"), 0644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	out, err := (&DiffCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	want := []string{
		"Diff (working tree)",   // scope label
		"hello.txt",             // stat header file
		"+changed line",         // body content
	}
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n  got: %s", s, out)
		}
	}
}

// TestDiff_StatOnlySuppressesContent — `--stat` skips body, only
// shows file list + +/- counts.
func TestDiff_StatOnlySuppressesContent(t *testing.T) {
	app, dir := newDiffTestApp(t)

	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("baseline\nchanged line\n"), 0644); err != nil {
		t.Fatalf("modify: %v", err)
	}

	out, err := (&DiffCommand{}).Execute(context.Background(), []string{"--stat"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "hello.txt") {
		t.Errorf("stat should include filename, got: %s", out)
	}
	// Stat output should NOT include actual diff body lines
	// (`+changed line` would appear if content snuck in).
	if strings.Contains(out, "+changed line") {
		t.Errorf("--stat should suppress body, but got: %s", out)
	}
}

// TestDiff_StagedFlag — only staged changes show; unstaged hidden.
func TestDiff_StagedFlag(t *testing.T) {
	app, dir := newDiffTestApp(t)

	// Stage one change.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("baseline\nstaged change\n"), 0644); err != nil {
		t.Fatalf("modify: %v", err)
	}
	cmd := exec.Command("git", "add", "hello.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}

	// Add a second, UNSTAGED change so we can verify --staged hides it.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("baseline\nstaged change\nunstaged tail\n"), 0644); err != nil {
		t.Fatalf("modify 2: %v", err)
	}

	out, err := (&DiffCommand{}).Execute(context.Background(), []string{"--staged"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "Diff (staged)") {
		t.Errorf("expected staged scope label, got: %s", out)
	}
	if !strings.Contains(out, "+staged change") {
		t.Errorf("staged body missing 'staged change': %s", out)
	}
	if strings.Contains(out, "+unstaged tail") {
		t.Errorf("--staged must hide unstaged additions, but got: %s", out)
	}
}

// TestDiff_FilePath — single-file scoping returns just that file's
// diff, not the full tree.
func TestDiff_FilePath(t *testing.T) {
	app, dir := newDiffTestApp(t)

	// Modify TWO files; ask /diff for only one.
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("baseline\nA\n"), 0644); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "world.txt"), []byte("WORLD\n"), 0644); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	// Track world.txt so it's in the working tree (not just untracked).
	cmd := exec.Command("git", "add", "world.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add world: %v: %s", err, out)
	}

	out, err := (&DiffCommand{}).Execute(context.Background(), []string{"hello.txt"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "Diff (hello.txt)") {
		t.Errorf("expected file-scoped label, got: %s", out)
	}
	if strings.Contains(out, "world.txt") {
		t.Errorf("file-scoped diff must hide other files, got: %s", out)
	}
}

// TestDiff_TruncationGuard — when diff exceeds the cap, the result
// is truncated and includes a hint pointing to `git diff` for full.
func TestDiff_TruncationGuard(t *testing.T) {
	huge := strings.Repeat("x\n", 50_000)
	got := truncateDiffOutput(huge)
	if len(got) >= len(huge) {
		t.Errorf("truncation should shrink output, got %d / orig %d", len(got), len(huge))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation tail missing, got tail: %q", got[len(got)-100:])
	}
}

// TestDiff_NotAGitRepo — gracefully report rather than 500.
func TestDiff_NotAGitRepo(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = t.TempDir() // empty dir, no git init

	out, err := (&DiffCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Not a git repository") {
		t.Errorf("non-git dir should say so, got: %s", out)
	}
}
