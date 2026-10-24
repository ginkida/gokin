//go:build unix

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRequiredGitProbeDistinguishesCancellationFromNonRepository(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /bin/sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if repo, err := CheckGitRepo(ctx, root); repo || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout became not-a-repo: repo=%v err=%v", repo, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'fatal: not a git repository' >&2\nexit 128\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if repo, err := CheckGitRepo(t.Context(), root); repo || err != nil {
		t.Fatalf("real not-a-repo: repo=%v err=%v", repo, err)
	}
}

func TestStartupGitProbesAreBounded(t *testing.T) {
	for _, operation := range []string{"repository", "worktree"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "pid")
			if err := os.WriteFile(filepath.Join(root, "git"), []byte("#!/bin/sh\necho $$ > '"+pidFile+"'\nexec /bin/sleep 30\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if operation == "repository" {
					IsGitRepo(root)
				} else {
					GetMainWorktreeRoot(root)
				}
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("startup git probe waited without a deadline")
				data, _ := os.ReadFile(pidFile)
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
					if process, err := os.FindProcess(pid); err == nil {
						_ = process.Kill()
					}
				}
				<-done
			}
		})
	}
}
