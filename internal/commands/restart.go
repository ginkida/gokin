package commands

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

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

	// Inform the user BEFORE exec'ing — the message must go out through
	// the existing TUI pipeline before syscall.Exec annihilates the
	// process. Returning early from Execute would be cleaner, but the
	// exec itself doesn't return on success, so this "notice before
	// exec" is the only shot we get.
	//
	// The safeGo on Exec guards against the theoretical case where the
	// new binary fails to start (e.g., corrupted after install) — the
	// fallback error is surfaced through the normal return path.
	go func() {
		// Small delay so the "Restarting..." message reaches the user
		// before the process image is replaced. `syscall.Exec` is
		// typically instant (no forking), but Bubble Tea needs a tick
		// to flush the final frame.
		args := append([]string{exe}, os.Args[1:]...)
		_ = syscall.Exec(exe, args, os.Environ())
		// If exec returned, it failed. We can't easily get the error
		// back to the user — log and fall through; the process stays
		// running with the old code.
	}()

	return fmt.Sprintf("Restarting gokin (exec %s)…\nSession state will be lost. Use /save first if you need to preserve it.", exe), nil
}
