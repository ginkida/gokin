//go:build windows

package security

import (
	"os/exec"
	"strconv"
)

func configureSandboxProcessGroup(_ *exec.Cmd) {}

func terminateSandboxProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}

// applySandbox applies basic process isolation for Windows
func (sc *SandboxedCommand) applySandbox(workDir string) error {
	// Windows doesn't support Unix process groups
	// Basic isolation is handled by the OS
	return nil
}
