//go:build windows

package tasks

import (
	"os/exec"
)

// setProcAttr sets Windows-specific process attributes
func setProcAttr(cmd *exec.Cmd) {
	// No special process attributes needed on Windows
}

// killProcessGroup kills the process on Windows
func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
