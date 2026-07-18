//go:build windows

package tools

import (
	"os/exec"
	"strconv"
	"time"

	"gokin/internal/logging"
)

// setBashProcAttr sets Windows-specific process attributes
func setBashProcAttr(cmd *exec.Cmd) {
	// No special process attributes needed on Windows
}

// killBashProcessGroup kills the entire process tree on Windows.
func killBashProcessGroup(cmd *exec.Cmd, gracePeriod time.Duration, done <-chan struct{}) {
	if cmd.Process == nil {
		return
	}

	// taskkill /T covers descendants; Process.Kill alone terminates only the
	// shell leader and leaves grandchildren running. /F is required because a
	// cancelled foreground command cannot be allowed to outlive the tool call.
	err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run()
	if err != nil {
		if killErr := cmd.Process.Kill(); killErr != nil {
			logging.Warn("failed to kill process tree", "taskkill_error", err, "kill_error", killErr)
		}
	}
}

// reapLeftoverBashDescendants is a no-op on Windows: the taskkill /T kill
// path covers descendants on cancellation, and Windows has no process-group
// orphan semantics equivalent to the Unix `yes &` leak this guards against.
func reapLeftoverBashDescendants(_ *exec.Cmd) {}
