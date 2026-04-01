package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGitRepo creates a temporary git repo with an initial commit.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", dir},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}

	// Create initial commit
	f := filepath.Join(dir, "README.md")
	os.WriteFile(f, []byte("# test"), 0644)
	exec.Command("git", "-C", dir, "add", ".").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "init").Run()

	return dir
}

func TestDetectWorktree_MainRepo(t *testing.T) {
	dir := setupGitRepo(t)
	wt := DetectWorktree(dir)
	if wt == nil {
		t.Fatal("expected worktree info, got nil")
	}
	if !wt.IsMainWorktree {
		t.Error("expected IsMainWorktree=true for main repo")
	}
	if wt.Branch == "" {
		t.Error("expected non-empty branch")
	}
}

func TestDetectWorktree_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	wt := DetectWorktree(dir)
	if wt != nil {
		t.Error("expected nil for non-git directory")
	}
}

func TestListWorktrees_SingleWorktree(t *testing.T) {
	dir := setupGitRepo(t)
	trees := ListWorktrees(dir)
	if len(trees) == 0 {
		t.Fatal("expected at least 1 worktree")
	}
	if !trees[0].IsMainWorktree {
		t.Error("first worktree should be main")
	}
}

func TestListWorktrees_WithAdditionalWorktree(t *testing.T) {
	dir := setupGitRepo(t)

	// Create a new branch and worktree
	wtDir := filepath.Join(t.TempDir(), "wt-feature")
	exec.Command("git", "-C", dir, "branch", "feature").Run()
	out, err := exec.Command("git", "-C", dir, "worktree", "add", wtDir, "feature").CombinedOutput()
	if err != nil {
		t.Skipf("git worktree not supported: %v\n%s", err, out)
	}
	defer exec.Command("git", "-C", dir, "worktree", "remove", "--force", wtDir).Run()

	trees := ListWorktrees(dir)
	if len(trees) < 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(trees))
	}

	found := false
	for _, tree := range trees {
		if strings.Contains(tree.Path, "wt-feature") {
			found = true
			if tree.Branch != "feature" {
				t.Errorf("expected branch 'feature', got %q", tree.Branch)
			}
		}
	}
	if !found {
		t.Error("feature worktree not found in list")
	}
}

func TestGetMainWorktreeRoot(t *testing.T) {
	dir := setupGitRepo(t)
	root := GetMainWorktreeRoot(dir)
	absDir, _ := filepath.Abs(dir)
	absRoot, _ := filepath.Abs(root)
	if absRoot != absDir {
		t.Errorf("expected %s, got %s", absDir, absRoot)
	}
}

func TestGetMainWorktreeRoot_NotGit(t *testing.T) {
	dir := t.TempDir()
	root := GetMainWorktreeRoot(dir)
	if root != dir {
		t.Errorf("non-git dir should return itself, got %s", root)
	}
}
