//go:build unix

package security

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureSandboxProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateSandboxProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		// Fallback protects callers if process-group setup was rejected by the
		// host. Run still Waits/reaps the leader after this returns.
		_ = cmd.Process.Kill()
	}
}
