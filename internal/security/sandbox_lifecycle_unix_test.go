//go:build unix

package security

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSandboxNormalExitReapsDescendantsHoldingOutputPipes(t *testing.T) {
	dir := t.TempDir()
	sc := newUnsandboxedLifecycleCommand(t, context.Background(), dir,
		"(trap '' TERM; sleep 30) & printf complete; printf diagnostic >&2")
	result := sc.Run(time.Second)
	if result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("successful shell waited for its background child's pipes: %+v", result)
	}
	if string(result.Stdout) != "complete" || string(result.Stderr) != "diagnostic" {
		t.Fatalf("lost output during descendant cleanup: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestSandboxRunDrainsOutputAfterLeaderExit(t *testing.T) {
	// Each stream exceeds a pipe buffer. Wait must reap the leader without
	// closing the read ends or discarding the tail of either stream.
	sc := newUnsandboxedLifecycleCommand(t, context.Background(), t.TempDir(),
		"for ((i=0; i<8192; i++)); do printf 'stdout-0123456789'; printf 'stderr-0123456789' >&2; done")
	result := sc.Run(5 * time.Second)
	if result.Error != nil || result.ExitCode != 0 {
		t.Fatalf("Run: %+v", result)
	}
	if string(result.Stdout) != strings.Repeat("stdout-0123456789", 8192) ||
		string(result.Stderr) != strings.Repeat("stderr-0123456789", 8192) {
		t.Fatalf("output truncated: stdout=%d stderr=%d", len(result.Stdout), len(result.Stderr))
	}
}

func TestSandboxCancellationPreservesCompletedOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	sc := newUnsandboxedLifecycleCommand(t, ctx, dir,
		"printf complete; printf diagnostic >&2; printf ready > "+sandboxShellQuote(ready)+"; sleep 30")
	go func() {
		for {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	result := sc.Run(5 * time.Second)
	if !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("cancellation reason lost: %+v", result)
	}
	if string(result.Stdout) != "complete" || string(result.Stderr) != "diagnostic" {
		t.Fatalf("cancellation lost output: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestSandboxRunZeroTimeoutWaitsWithoutLimit(t *testing.T) {
	dir := t.TempDir()
	sc := newUnsandboxedLifecycleCommand(t, context.Background(), dir, "sleep 0.05; printf complete")

	result := sc.Run(0)
	if result.Error != nil {
		t.Fatalf("Run(0): %v", result.Error)
	}
	if result.ExitCode != 0 || string(result.Stdout) != "complete" {
		t.Fatalf("Run(0) result=%+v stdout=%q, want successful complete output", result, result.Stdout)
	}
}

func TestReadWithTimeoutZeroMeansNoLimit(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("delayed"))
		_ = w.Close()
	}()

	data, err := readWithTimeout(r, 0)
	if err != nil {
		t.Fatalf("readWithTimeout(0): %v", err)
	}
	if string(data) != "delayed" {
		t.Fatalf("readWithTimeout(0)=%q, want delayed", data)
	}
}

func TestSandboxCancellationHasNoPostReturnDescendantMutation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		timeout    time.Duration
	}{
		{
			name: "context cancellation",
			newContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(25*time.Millisecond, cancel)
				return ctx, cancel
			},
		},
		{
			name: "run timeout",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			timeout: 25 * time.Millisecond,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "late-mutation")
			ctx, cancel := tc.newContext()
			defer cancel()

			// wait is a shell builtin, so killing only the bash leader leaves just
			// the short-lived TERM-ignoring descendant. It attempts a harmless
			// mutation after Run should already have returned.
			command := "(trap '' TERM; sleep 0.3; printf late > " + sandboxShellQuote(marker) + ") >/dev/null 2>&1 & wait"
			sc := newUnsandboxedLifecycleCommand(t, ctx, dir, command)
			result := sc.Run(tc.timeout)
			if result.Error == nil && result.ExitCode == 0 {
				t.Fatalf("cancelled command unexpectedly succeeded: %+v", result)
			}

			before, beforeErr := os.ReadFile(marker)
			time.Sleep(400 * time.Millisecond)
			after, afterErr := os.ReadFile(marker)
			if !sameSandboxTestFileState(before, beforeErr, after, afterErr) {
				t.Fatalf(
					"descendant mutated %s after Run returned: before=(%q, %v), after=(%q, %v)",
					marker, before, beforeErr, after, afterErr,
				)
			}
		})
	}
}

func TestSandboxPreCancelledContextDoesNotStartProcess(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sc := newUnsandboxedLifecycleCommand(t, ctx, dir, "printf started > "+sandboxShellQuote(marker))

	result := sc.Run(0)
	if result.Error == nil {
		t.Fatalf("pre-cancelled Run result=%+v, want cancellation error", result)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("pre-cancelled command started: %v", err)
	}
}

func newUnsandboxedLifecycleCommand(t *testing.T, ctx context.Context, dir, command string) *SandboxedCommand {
	t.Helper()
	config := DefaultSandboxConfig()
	config.Enabled = false // Lifecycle semantics must not depend on namespace privileges.
	sc, err := NewSandboxedCommand(ctx, dir, command, config)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func sandboxShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func sameSandboxTestFileState(before []byte, beforeErr error, after []byte, afterErr error) bool {
	beforeMissing := os.IsNotExist(beforeErr)
	afterMissing := os.IsNotExist(afterErr)
	if beforeMissing || afterMissing {
		return beforeMissing && afterMissing
	}
	return beforeErr == nil && afterErr == nil && string(before) == string(after)
}
