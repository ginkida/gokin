package commands

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
)

// restartExecFn is the process-replacing call the command makes. In
// production it's the real syscall.Exec, which replaces the current
// process image with the freshly-installed gokin binary. Tests override
// it to a no-op so running `RestartCommand.Execute` doesn't actually
// re-exec the go test binary (which previously caused an 11-minute
// timeout when the goroutine fired inside the test runner).
var restartExecFn = syscall.Exec

// restartExitFn is the other kill-the-process call — used on Windows
// where syscall.Exec returns EWINDOWS and we fall back to clean exit.
// Same tests-override-this rationale.
var restartExitFn = os.Exit

// restartSleepFn bounds the "wait for TUI flush" delay before we
// tear down the process. Tests set it to an instant no-op so the
// goroutine completes before the test finishes — otherwise we'd leak
// a goroutine that may call Exec/Exit after the test returns.
var restartSleepFn = func() { time.Sleep(250 * time.Millisecond) }

// RestartCommand re-execs the current binary so a freshly-installed
// update takes effect without the user manually quitting. The use case
// is the post-`/update install` flow: the installer swaps the binary on
// disk, but the running process still holds the old code until it exits
// — `/restart` makes that exit+relaunch one keystroke instead of a
// tedious "Ctrl+C, up-arrow, Enter" sequence.
//
// Implementation: `syscall.Exec` on the current binary path replaces
// this process image entirely. No leftover goroutines, no half-cleaned
// state. Session history is lost (same as any restart) — users with
// unsaved work should /save first.
type RestartCommand struct{}

func (c *RestartCommand) Name() string { return "restart" }
func (c *RestartCommand) Description() string {
	return "Re-exec gokin (apply updates without manual quit)"
}
func (c *RestartCommand) Usage() string { return "/restart" }

func (c *RestartCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category: CategoryAuthSetup,
		Icon:     "restart",
		Priority: 8, // right below /update
	}
}

func (c *RestartCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	// `os.Executable` resolves the real path of the current binary even
	// when invoked via a relative path or symlink. Falls back to
	// `os.Args[0]` if the platform doesn't support it (very unusual).
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}

	// Windows has no execve(2) equivalent — Go's syscall.Exec returns
	// EWINDOWS and leaves the process alone. Rather than silently fail,
	// we exit cleanly so the user sees a clean terminal and can
	// relaunch. The swapped binary is already on disk (the installer
	// did the rename in a prior step), so "exit + relaunch manually"
	// picks up the new version.
	if runtime.GOOS == "windows" {
		go func() {
			restartSleepFn()
			restartExitFn(0)
		}()
		return "Exiting gokin… relaunch manually to apply the update (Windows doesn't support in-place exec).\nSession state will be lost. Use /save first if you need to preserve it.", nil
	}

	// Unix path: re-exec via syscall.Exec. The process image is
	// replaced — no leftover goroutines, no half-cleaned state. If
	// Exec returns (rare: permissions issue, corrupted binary), we
	// fall back to os.Exit so the user gets a deterministic "process
	// gone" signal and can relaunch manually.
	go func() {
		restartSleepFn() // let the TUI flush the notice
		args := append([]string{exe}, os.Args[1:]...)
		if err := restartExecFn(exe, args, os.Environ()); err != nil {
			// Exec refused — the process is still alive with the old
			// code. Forced exit is better UX than a silently stuck
			// /restart that leaves users wondering.
			fmt.Fprintf(os.Stderr, "gokin: /restart exec failed (%v); exiting — please relaunch manually\n", err)
			restartExitFn(1)
		}
	}()

	return fmt.Sprintf("Restarting gokin (exec %s)…\nSession state will be lost. Use /save first if you need to preserve it.", exe), nil
}
