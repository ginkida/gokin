package commands

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// runGitCommandCtx must honor context cancellation so the user pressing Esc
// during a long-running git invocation actually kills the underlying process.
// Pre-v0.78.4 the inspect commands (/grep /branches /log /diff) called the
// non-context helper, so cancellation was a no-op until git returned.
func TestRunCommandCtx_CancelledBeforeRun_ErrorsFast(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep semantics differ on Windows")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel — exec.CommandContext should refuse / immediately kill

	start := time.Now()
	_, err := runCommandCtx(ctx, "", "sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on pre-cancelled ctx, got nil")
	}
	// Must finish in well under the requested 30s — if the helper ignored ctx,
	// this would be ~30s.
	if elapsed > 5*time.Second {
		t.Errorf("cancellation didn't propagate: took %v (limit 5s)", elapsed)
	}
}

// Cancelling mid-run also kills the child. The test starts a long sleep,
// cancels after 50ms, and asserts the call returns within seconds.
func TestRunCommandCtx_CancelMidRun_KillsProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep semantics differ on Windows")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep binary not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := runCommandCtx(ctx, "", "sleep", "30")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error after cancel, got nil")
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancel didn't kill child: took %v (limit 5s)", elapsed)
	}
	// Either ctx.Err() or an *exec.ExitError surfaced from SIGKILL is fine —
	// we just want to know it didn't run to completion.
	if !errors.Is(err, context.Canceled) {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Logf("unusual error shape (acceptable): %v", err)
		}
	}
}

// The non-context wrapper still works the old way — so pre-existing callers
// in git.go (commit/PR flows) keep their behavior.
func TestRunCommand_NoCtx_StillWorks(t *testing.T) {
	if _, err := exec.LookPath("echo"); err != nil {
		t.Skip("echo binary not available")
	}
	out, err := runCommand("", "echo", "ok")
	if err != nil {
		t.Fatalf("runCommand(echo): %v", err)
	}
	if want := "ok\n"; out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}
