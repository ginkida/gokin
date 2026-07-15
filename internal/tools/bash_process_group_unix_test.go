//go:build unix

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestKillBashProcessGroupEscalatesAfterLeaderExited(t *testing.T) {
	// The shell leader exits immediately while a TERM-ignoring descendant stays
	// in its process group. cmd.Wait being done is not evidence that the group
	// is gone.
	cmd := exec.Command("bash", "-c", `(trap '' TERM; exec sleep 2) >/dev/null 2>&1 & printf '%s\n' "$!"`)
	setBashProcAttr(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", out, err)
	}
	defer func() { _ = syscall.Kill(childPID, syscall.SIGKILL) }()
	if !bashTestProcessExists(childPID) {
		t.Fatal("TERM-ignoring descendant exited before the test")
	}

	done := make(chan struct{})
	close(done) // cmd.Output already waited for the shell leader.
	killBashProcessGroup(cmd, 40*time.Millisecond, done)
	if !waitForBashTestProcessExit(childPID, 500*time.Millisecond) {
		t.Fatal("process-group cleanup returned while a descendant was still alive")
	}
}

func TestBashToolCancellationHasNoPostReturnDescendantMutation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "buffered"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "late-mutation")
			tool := NewBashTool(dir)
			tool.SetTimeout(25 * time.Millisecond)

			// The background subshell ignores TERM and attempts a harmless write well
			// after the shell leader is killed. It redirects inherited pipes so parent
			// Wait can finish while the descendant remains alive.
			command := "(trap '' TERM; sleep 0.4; printf late > " + shellSingleQuote(marker) + ") >/dev/null 2>&1 & sleep 2"
			ctx := context.Background()
			if streaming {
				ctx = ContextWithProgressCallback(ctx, func(float64, string) {})
			}
			result, err := tool.Execute(ctx, map[string]any{"command": command})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success {
				t.Fatal("timed-out command unexpectedly succeeded")
			}

			before, beforeErr := os.ReadFile(marker)
			time.Sleep(500 * time.Millisecond)
			after, afterErr := os.ReadFile(marker)
			if !sameBashTestFileState(before, beforeErr, after, afterErr) {
				t.Fatalf(
					"descendant mutated %s after BashTool returned: before=(%q, %v), after=(%q, %v)",
					marker, before, beforeErr, after, afterErr,
				)
			}
		})
	}
}

func TestBashToolPreCancelledContextDoesNotStartProcess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	tool := NewBashTool(dir)
	tool.SetTimeout(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := tool.Execute(ctx, map[string]any{
		"command": "printf started > " + shellSingleQuote(marker),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("pre-cancelled command unexpectedly succeeded")
	}
	if !strings.Contains(result.Error, "cancelled") {
		t.Fatalf("pre-cancelled command reported the wrong reason: %q", result.Error)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-cancelled context started the command: %v", err)
	}
}

func TestBashToolMidFlightCancellationIsNotReportedAsTimeout(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.SetTimeout(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	started := time.Now()
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()

	result, err := tool.Execute(ctx, map[string]any{"command": "sleep 5"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "cancelled") || strings.Contains(result.Error, "timed out") {
		t.Fatalf("cancelled command result = success %v error %q", result.Success, result.Error)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func bashTestProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func waitForBashTestProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !bashTestProcessExists(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !bashTestProcessExists(pid)
}

func sameBashTestFileState(before []byte, beforeErr error, after []byte, afterErr error) bool {
	beforeMissing := os.IsNotExist(beforeErr)
	afterMissing := os.IsNotExist(afterErr)
	if beforeMissing || afterMissing {
		return beforeMissing && afterMissing
	}
	return beforeErr == nil && afterErr == nil && string(before) == string(after)
}
