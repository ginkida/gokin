//go:build unix

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gokin/internal/gitexec"
)

func TestSnapshotGitCancellationKillsDescendants(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child")
	stub := filepath.Join(root, "git")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nsleep 300 >/dev/null 2>&1 &\necho $! > '"+pidFile+"'\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(gitexec.WithExecutable(t.Context(), stub))
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = gitOutput(ctx, root, 1024, "status") }()
	var pid int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if out, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(out)))
			if pid != 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("Git stub did not spawn child")
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Git probe did not stop")
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Git descendant %d survived probe cancellation", pid)
}
