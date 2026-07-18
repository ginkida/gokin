//go:build unix

package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// v0.100.104: exec.CommandContext's default Cancel SIGKILLs the LEADER only —
// `npm test`/`go test`/arbitrary delta-check commands spawn children that a
// timeout then orphans forever (the orphaned-`yes` class on the runner
// paths). KillProcessGroupOnCancel must take the whole group down.
func TestKillProcessGroupOnCancel_TimeoutKillsDescendants(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The shell (leader) spawns a redirected long sleeper (child in the same
	// group) and then blocks itself; we print the child's pid first.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 >/dev/null 2>&1 & echo CHILD=$!; sleep 300")
	KillProcessGroupOnCancel(cmd)

	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 128)
	n, _ := out.Read(buf)
	var childPid int
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "CHILD=") {
			fmt.Sscanf(strings.TrimSpace(line), "CHILD=%d", &childPid)
		}
	}
	if childPid == 0 {
		t.Fatalf("could not parse child pid from %q", string(buf[:n]))
	}

	cancel()
	_ = cmd.Wait()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPid, 0); err != nil {
			return // gone — the group kill took the descendant too
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(childPid, syscall.SIGKILL)
	t.Fatalf("descendant %d survived context cancellation — orphan leak", childPid)
}
