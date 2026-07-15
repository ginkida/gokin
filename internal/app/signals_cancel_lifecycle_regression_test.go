package app

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

const signalCancelLifecycleHelper = "GOKIN_SIGNAL_CANCEL_LIFECYCLE_HELPER"

// The OS-interrupt path advertises the same first-press cancellation semantics
// as Esc/Ctrl+C in the TUI. It must therefore use the complete cancellation
// lifecycle, including dropping type-ahead, rather than invoking the raw
// context cancel function and allowing the finalizer to auto-start queued work.
func TestOSInterruptUsesCompleteForegroundCancellationLifecycle(t *testing.T) {
	if os.Getenv(signalCancelLifecycleHelper) == "1" {
		runSignalCancelLifecycleHelper(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestOSInterruptUsesCompleteForegroundCancellationLifecycle$")
	cmd.Env = append(os.Environ(), signalCancelLifecycleHelper+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("interrupt cancellation lifecycle failed: %v\n%s", err, output)
	}
}

func runSignalCancelLifecycleHelper(t *testing.T) {
	a := &App{ctx: context.Background()}
	a.processing = true
	cancelled := make(chan struct{})
	a.processingCancel = func() {
		select {
		case <-cancelled:
		default:
			close(cancelled)
		}
	}
	if _, ok := a.enqueuePending("queued type-ahead"); !ok {
		t.Fatal("fixture could not enqueue type-ahead")
	}

	cleanup := a.setupSignalHandler()
	defer cleanup()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("SIGINT did not cancel foreground context")
	}
	if got := a.pendingCount(); got != 0 {
		t.Fatalf("SIGINT left %d queued request(s) to auto-start after explicit cancellation", got)
	}
}
