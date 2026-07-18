//go:build unix

package tools

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"
)

// v0.100.104 field report: a foreground bash command that backgrounds a child
// (`yes >/dev/null &` in a script) exited normally, and the child was
// ORPHANED FOREVER — the process-group kill only ran on cancel/timeout, so a
// leaked `yes` pinned a CPU core long after the app exited. After a normal
// exit, group survivors must be reaped.
func TestBashNormalExitReapsBackgroundedDescendants(t *testing.T) {
	bt := NewBashTool(t.TempDir())

	// Backgrounds an effectively-infinite sleep with its output REDIRECTED
	// (the real field shape — `yes >/dev/null &`): the child holds no
	// inherited pipe, so the shell exits 0 immediately and Wait returns,
	// leaving the sleeper alive in the process group.
	res, err := bt.Execute(context.Background(), map[string]any{
		"command": "sleep 300 >/dev/null 2>&1 & echo CHILD=$!",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("command failed: %s / %s", res.Content, res.Error)
	}
	var childPid int
	for _, line := range strings.Split(res.Content, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CHILD=") {
			fmt.Sscanf(strings.TrimSpace(line), "CHILD=%d", &childPid)
		}
	}
	if childPid == 0 {
		t.Fatalf("could not parse child pid from output: %q", res.Content)
	}

	// The backgrounded sleeper must be gone shortly after the tool returned
	// (the reap allows a 2s TERM grace before KILL).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPid, 0); err != nil {
			return // process is gone — reaped
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Clean up ourselves so a failing test doesn't leak the sleeper.
	_ = syscall.Kill(childPid, syscall.SIGKILL)
	t.Fatalf("backgrounded child %d survived the tool call — orphan leak", childPid)
}
