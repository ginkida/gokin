//go:build unix

package donegate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunCommandWithTimeoutKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	command := "sleep 300 & child=$!; printf '%s' \"$child\" > " +
		shellQuoteForDoneGateTest(pidFile) + "; wait \"$child\""

	_, err := runCommandWithTimeout(100*time.Millisecond, "sh", "-c", command)
	if err == nil {
		t.Fatal("timed command unexpectedly succeeded")
	}
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("child pid was not published: %v", readErr)
	}
	childPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		t.Fatalf("parse child pid: %v", parseErr)
	}
	defer func() { _ = syscall.Kill(childPID, syscall.SIGKILL) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("done-gate command descendant %d survived timeout", childPID)
}

func shellQuoteForDoneGateTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
