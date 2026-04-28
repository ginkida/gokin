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

// newBlameTestApp builds a tempdir git repo with a multi-line file
// committed across two authors so /blame has interesting output.
func newBlameTestApp(t *testing.T) (*fakeAppForAuth, string) {
	t.Helper()
	dir := t.TempDir()

	for _, c := range [][]string{
		{"git", "init", "--quiet", "-b", "main"},
		{"git", "config", "user.email", "alice@example.com"},
		{"git", "config", "user.name", "Alice"},
		{"git", "config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v: %s", c, err, out)
		}
	}

	// First commit: 3-line file by Alice.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alice 1\nalice 2\nalice 3\n"), 0644); err != nil {
		t.Fatalf("write1: %v", err)
	}
	for _, c := range [][]string{
		{"git", "add", "f.txt"},
		{"git", "commit", "--quiet", "-m", "init by alice"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit alice: %v: %s", err, out)
		}
	}

	// Second commit: Bob extends the file by 2 more lines.
	for _, c := range [][]string{
		{"git", "config", "user.email", "bob@example.com"},
		{"git", "config", "user.name", "Bob"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("set bob: %v: %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alice 1\nalice 2\nalice 3\nbob 4\nbob 5\n"), 0644); err != nil {
		t.Fatalf("write2: %v", err)
	}
	for _, c := range [][]string{
		{"git", "add", "f.txt"},
		{"git", "commit", "--quiet", "-m", "extend by bob"},
	} {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit bob: %v: %s", err, out)
		}
	}

	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = dir
	return app, dir
}

// TestBlame_FullFile shows authorship for every line; both Alice's
// and Bob's commits should be represented.
func TestBlame_FullFile(t *testing.T) {
	app, _ := newBlameTestApp(t)

	out, err := (&BlameCommand{}).Execute(context.Background(), []string{"f.txt"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	for _, want := range []string{"Alice", "Bob", "alice 1", "bob 4", "Blame f.txt:"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

// TestBlame_LineRange — N-M form scopes blame to a window. Bob's
// lines (4-5) should show, Alice's (1-3) should not.
func TestBlame_LineRange(t *testing.T) {
	app, _ := newBlameTestApp(t)

	out, err := (&BlameCommand{}).Execute(context.Background(), []string{"f.txt", "4-5"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "Bob") {
		t.Errorf("expected Bob in range 4-5 output, got: %s", out)
	}
	if strings.Contains(out, "alice 1") {
		t.Errorf("range 4-5 should not include line 1, got: %s", out)
	}
	if !strings.Contains(out, "f.txt:4-5") {
		t.Errorf("header should reflect range, got: %s", out)
	}
}

// TestBlame_SingleLine — single integer arg picks one line.
func TestBlame_SingleLine(t *testing.T) {
	app, _ := newBlameTestApp(t)

	out, err := (&BlameCommand{}).Execute(context.Background(), []string{"f.txt", "1"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "alice 1") {
		t.Errorf("line 1 should show 'alice 1', got: %s", out)
	}
	if strings.Contains(out, "alice 2") {
		t.Errorf("single-line blame leaked line 2, got: %s", out)
	}
	if !strings.Contains(out, "f.txt:1") {
		t.Errorf("header should reflect single line, got: %s", out)
	}
}

// TestBlame_TwoIntForm — `N M` (whitespace-separated) is equivalent
// to `N-M`.
func TestBlame_TwoIntForm(t *testing.T) {
	app, _ := newBlameTestApp(t)

	out, err := (&BlameCommand{}).Execute(context.Background(), []string{"f.txt", "1", "2"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	if !strings.Contains(out, "alice 1") || !strings.Contains(out, "alice 2") {
		t.Errorf("expected both lines 1-2, got: %s", out)
	}
	if strings.Contains(out, "alice 3") {
		t.Errorf("range 1-2 should not include line 3, got: %s", out)
	}
}

// TestBlame_NoArgs prints usage hint rather than crashing or running
// blame on a default file.
func TestBlame_NoArgs(t *testing.T) {
	app, _ := newBlameTestApp(t)

	out, err := (&BlameCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Usage: /blame") {
		t.Errorf("no-arg invocation should print usage, got: %s", out)
	}
}

// TestBlame_NotAGitRepo — fall back to friendly error rather than
// crashing in a non-git working directory.
func TestBlame_NotAGitRepo(t *testing.T) {
	app := newAuthApp(&config.Config{})
	app.fakeAppForMCP.workDir = t.TempDir()

	out, err := (&BlameCommand{}).Execute(context.Background(), []string{"foo.go"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Not a git repository") {
		t.Errorf("non-git dir should say so, got: %s", out)
	}
}

// TestBlame_BadRange — malformed range should be rejected with a
// usage-style hint, not propagated to git blame.
func TestBlame_BadRange(t *testing.T) {
	app, _ := newBlameTestApp(t)

	cases := []struct {
		args []string
		want string
	}{
		{[]string{"f.txt", "5-2"}, "Invalid range"},     // M < N
		{[]string{"f.txt", "abc"}, "Invalid line"},       // non-numeric
		{[]string{"f.txt", "0"}, "Invalid line"},         // zero rejected
		{[]string{"f.txt", "-3"}, "Invalid"},             // negative
	}
	for _, tc := range cases {
		out, err := (&BlameCommand{}).Execute(context.Background(), tc.args, app)
		if err != nil {
			t.Fatalf("args %v: unexpected err: %v", tc.args, err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("args %v: expected %q in output, got: %s", tc.args, tc.want, out)
		}
	}
}

// TestBlame_Truncation — a giant file should be capped with a tail
// hint rather than flooding scrollback.
func TestBlame_Truncation(t *testing.T) {
	huge := strings.Repeat("hash (Author 2026-01-01) line\n", blameMaxLines+50)
	got, truncated := truncateBlameOutput(strings.TrimRight(huge, "\n"))
	if !truncated {
		t.Errorf("huge body should report truncated=true")
	}
	gotLines := len(strings.Split(got, "\n"))
	if gotLines != blameMaxLines {
		t.Errorf("truncated output has %d lines, want %d", gotLines, blameMaxLines)
	}
}

// TestBlame_NonexistentFile — git blame returns an error; we surface
// it cleanly rather than panicking.
func TestBlame_NonexistentFile(t *testing.T) {
	app, _ := newBlameTestApp(t)

	out, err := (&BlameCommand{}).Execute(context.Background(), []string{"does-not-exist.txt"}, app)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "git blame failed") {
		t.Errorf("expected friendly error for missing file, got: %s", out)
	}
}
