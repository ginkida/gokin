//go:build windows

package tools

import (
	"os/exec"
	"time"

	"gokin/internal/logging"
)

// setBashProcAttr sets Windows-specific process attributes
func setBashProcAttr(cmd *exec.Cmd) {
	// No special process attributes needed on Windows
}

// killBashProcessGroup kills the process on Windows
func killBashProcessGroup(cmd *exec.Cmd, gracePeriod time.Duration, done <-chan struct{}) {
	if cmd.Process == nil {
		return
	}

	// On Windows, we just kill the process directly
	// There's no process group concept like Unix
	if err := cmd.Process.Kill(); err != nil {
		logging.Warn("failed to kill process", "error", err)
	}
}
