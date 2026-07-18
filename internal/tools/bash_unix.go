//go:build unix

package tools

import (
	"errors"
	"os/exec"
	"syscall"
	"time"

	"gokin/internal/logging"
)

// setBashProcAttr sets Unix-specific process attributes for proper cleanup
func setBashProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killBashProcessGroup attempts graceful shutdown with SIGTERM, then SIGKILL
// after timeout. cmd.Wait completing only proves the shell leader exited; it
// must never be used as evidence that descendants in the process group are
// gone.
func killBashProcessGroup(cmd *exec.Cmd, gracePeriod time.Duration, _ <-chan struct{}) {
	if cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid

	// First, try graceful shutdown with SIGTERM
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return // The process group is already gone.
		}
		// Process group kill failed, try individual process
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			logging.Debug("SIGTERM failed, trying SIGKILL", "error", err)
		}
	}

	// Wait for the process GROUP to disappear or the grace period to expire.
	// A TERM-ignoring descendant can remain after the shell's Wait is done.
	graceTimer := time.NewTimer(gracePeriod)
	defer graceTimer.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()

	for bashProcessGroupExists(pid) {
		select {
		case <-poll.C:
		case <-graceTimer.C:
			if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				// Fallback to killing just the process leader. The caller still
				// waits/reaps it before returning.
				if err := cmd.Process.Kill(); err != nil {
					logging.Warn("failed to kill process group", "error", err)
				}
			}
			return
		}
	}
}

func bashProcessGroupExists(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true
	}
	return !errors.Is(err, syscall.ESRCH)
}

// reapLeftoverBashDescendants sweeps process-group SURVIVORS after the shell
// leader exited NORMALLY. A foreground bash command that backgrounds a child
// (`yes >/dev/null &` in a script, a crashed installer leaving `yes | ...`'s
// writer behind) used to orphan it forever — the kill path only ran on
// cancel/timeout, so a leaked child could pin a CPU core long after the app
// exited (v0.100.104 field report: an orphaned `yes`). Foreground bash is not
// the sanctioned way to start long-lived background work (run_in_background
// is — the tasks manager owns those groups), so any survivor here is a leak:
// TERM, short grace, then KILL. Daemons that re-setsid into their own group
// (git auto-gc) have already left this group and are untouched.
func reapLeftoverBashDescendants(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if !bashProcessGroupExists(cmd.Process.Pid) {
		return
	}
	logging.Warn("bash command left background descendants after exiting — reaping the process group",
		"pgid", cmd.Process.Pid)
	killBashProcessGroup(cmd, 2*time.Second, nil)
}
