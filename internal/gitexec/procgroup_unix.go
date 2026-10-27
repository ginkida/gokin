//go:build unix

package gitexec

import (
	"os/exec"
	"syscall"
)

// KillProcessGroupOnCancel also kills Git's helpers (for example submodule
// status processes); CommandContext's default cancellation kills only Git.
func KillProcessGroupOnCancel(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
