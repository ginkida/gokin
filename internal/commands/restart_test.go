package commands

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// withStubbedRestartHooks replaces the package-level exec/exit/sleep
// hooks with no-ops for the duration of a test, so calling
// `RestartCommand.Execute` doesn't actually re-exec the go test binary
// or kill the test process (both of which happened before this was
// stubbed — the commands package test timed out at 10 minutes).
//
// Returns a cleanup that restores originals and a pair of counters the
// test can assert on (how many times exec/exit were invoked).
func withStubbedRestartHooks(t *testing.T) (execCalls, exitCalls *atomic.Int64, cleanup func()) {
	t.Helper()
	origExec := restartExecFn
	origExit := restartExitFn
	origSleep := restartSleepFn

	ec := &atomic.Int64{}
	xc := &atomic.Int64{}

	var wg sync.WaitGroup
	wg.Add(1)

	restartExecFn = func(_ string, _ []string, _ []string) error {
		ec.Add(1)
		wg.Done()
		return nil // pretend success; real syscall.Exec doesn't return
	}
	restartExitFn = func(_ int) {
		xc.Add(1)
		// Don't actually exit the test process.
	}
	restartSleepFn = func() {} // instant — no 250ms wall-clock delay

	return ec, xc, func() {
		// Wait for the background goroutine to finish before restoring
		// hooks; otherwise it may still be mid-call and use the wrong
		// function. Bounded by the test timeout.
		wg.Wait()
		restartExecFn = origExec
		restartExitFn = origExit
		restartSleepFn = origSleep
	}
}

// TestRestartCommand_UserGuidance pins the user-facing contract: when
// the user types /restart, they get a clear confirmation that spells
// out (a) gokin is about to re-exec, (b) session state will be lost,
// and (c) /save is the escape hatch.
func TestRestartCommand_UserGuidance(t *testing.T) {
	_, _, cleanup := withStubbedRestartHooks(t)
	defer cleanup()

	cmd := &RestartCommand{}
	out, err := cmd.Execute(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "Restarting gokin") && !strings.Contains(out, "Exiting gokin") {
		t.Errorf("output should announce the restart/exit, got: %q", out)
	}
	if !strings.Contains(out, "Session state will be lost") {
		t.Errorf("output should warn about session loss, got: %q", out)
	}
	if !strings.Contains(out, "/save") {
		t.Errorf("output should name /save as the escape hatch, got: %q", out)
	}
}

// TestRestartCommand_InvokesExec asserts the goroutine actually reaches
// the exec (or exit on Windows) hook — without this the test would
// happily pass on any build where the goroutine was silently dropped.
func TestRestartCommand_InvokesExec(t *testing.T) {
	execCalls, exitCalls, cleanup := withStubbedRestartHooks(t)

	cmd := &RestartCommand{}
	if _, err := cmd.Execute(context.Background(), nil, nil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	// cleanup() blocks on the goroutine's WaitGroup → when it returns,
	// the hook has been called. Doing the assertion afterward is the
	// deterministic way to observe the counter.
	cleanup()

	total := execCalls.Load() + exitCalls.Load()
	if total == 0 {
		t.Error("RestartCommand should invoke either restartExecFn (Unix) or restartExitFn (Windows)")
	}
}

// TestRestartCommand_Metadata verifies the command appears under the
// Auth & Setup category with a priority that puts it next to /update —
// that's where users expect to find "apply the update I just installed".
func TestRestartCommand_Metadata(t *testing.T) {
	cmd := &RestartCommand{}
	meta := cmd.GetMetadata()
	if meta.Category != CategoryAuthSetup {
		t.Errorf("category = %v, want CategoryAuthSetup (next to /update)", meta.Category)
	}
	if meta.Priority > 10 {
		t.Errorf("priority = %d, should be low enough to appear near top (≤10)", meta.Priority)
	}
	if cmd.Name() != "restart" {
		t.Errorf("name = %q, want %q", cmd.Name(), "restart")
	}
}
