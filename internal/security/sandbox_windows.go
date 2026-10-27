//go:build windows

package security

import (
	"fmt"
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
func wrapBashSandbox(_ *exec.Cmd, _ string, _ []sandboxMount, _ []string, _ string, _ bool) error {
	return fmt.Errorf("no bash isolation backend on Windows")
}
