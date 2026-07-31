//go:build unix

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeExecutableForGitToolTest(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestReviewChangesDoesNotReportCleanWhenUntrackedScanFails(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	writeExecutableForGitToolTest(t, binDir, "git", `
case " $* " in
  *" diff "*" --name-only "*) exit 0 ;;
  *" ls-files "*) echo "synthetic untracked scan failure" >&2; exit 2 ;;
  *) exit 0 ;;
esac
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	result, err := NewReviewChangesTool(workDir).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "failed to list untracked files") {
		t.Fatalf("failed untracked scan was reported as clean/success: success=%v error=%q content=%q", result.Success, result.Error, result.Content)
	}
	if strings.Contains(result.Content, "Working tree is clean") {
		t.Fatalf("failed review made a false clean claim: %q", result.Content)
	}
}

func TestGitPRChecksCancellationIsNotSuccessAndKillsDescendant(t *testing.T) {
	binDir := t.TempDir()
	workDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	writeExecutableForGitToolTest(t, binDir, "gh", `
sleep 300 &
child=$!
echo "$child" > "$GOKIN_TEST_CHILD_PID_FILE"
echo "partial checks report"
wait "$child"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOKIN_TEST_CHILD_PID_FILE", pidFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result ToolResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := NewGitPRTool(workDir).checksPR(ctx, map[string]any{"pr_number": "7"})
		done <- outcome{result: result, err: err}
	}()

	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		t.Fatal("fake gh did not publish its child pid")
	}

	cancel()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatal("gh checks did not return after cancellation")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.Success || !strings.Contains(got.result.Error, "interrupted") {
		t.Fatalf("cancelled checks produced false success: success=%v error=%q content=%q", got.result.Success, got.result.Error, got.result.Content)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Fatalf("gh descendant %d survived cancellation", childPID)
}
