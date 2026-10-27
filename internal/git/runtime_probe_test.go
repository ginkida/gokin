//go:build unix

package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeGitProbesRefreshAfterRepositoryChanges(t *testing.T) {
	installStartupGitFixture(t, `[ -f repository ] || { echo 'fatal: not a git repository' >&2; exit 128; }
case "$*" in
  'rev-parse --git-dir') echo .git ;;
  'rev-parse --abbrev-ref HEAD') /bin/cat branch ;;
esac
`)
	root := t.TempDir()
	provider := NewGitContextProvider(root)
	if _, err := provider.GetContext(t.Context()); err == nil {
		t.Fatal("fixture unexpectedly starts in a repository")
	}
	if err := os.WriteFile(filepath.Join(root, "repository"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	branch := filepath.Join(root, "branch")
	if err := os.WriteFile(branch, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !IsGitRepo(root) {
		t.Error("runtime repository probe retained the earlier failure")
	}
	if gc, err := provider.GetContext(t.Context()); err != nil || gc.Branch != "first" {
		t.Errorf("git context did not recover: context=%+v err=%v", gc, err)
	}
	if got := GetCurrentBranch(root); got != "first" {
		t.Fatalf("initial branch = %q", got)
	}
	if err := os.WriteFile(branch, []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := GetCurrentBranch(root); got != "second" {
		t.Errorf("runtime branch probe retained %q", got)
	}
	if err := os.Remove(filepath.Join(root, "repository")); err != nil {
		t.Fatal(err)
	}
	if IsGitRepo(root) {
		t.Error("runtime repository probe retained a removed repository")
	}
}
