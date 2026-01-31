//go:build unix

package tools

import (
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

// killBashProcessGroup attempts graceful shutdown with SIGTERM, then SIGKILL after timeout.
func killBashProcessGroup(cmd *exec.Cmd, gracePeriod time.Duration) {
	if cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid

	// First, try graceful shutdown with SIGTERM
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		// Process group kill failed, try individual process
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			logging.Debug("SIGTERM failed, trying SIGKILL", "error", err)
		}
	}

	// Wait briefly for graceful shutdown
	done := make(chan struct{})
	go func() {
		time.Sleep(gracePeriod)
		close(done)
	}()

	select {
	case <-done:
		// Grace period expired - escalate to SIGKILL
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			// Fallback to killing just the process
			if err := cmd.Process.Kill(); err != nil {
				logging.Warn("failed to kill process", "error", err)
			}
		}
	}
}
