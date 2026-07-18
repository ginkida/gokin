//go:build unix

package tools

import (
	"os/exec"
	"syscall"
)

// KillProcessGroupOnCancel puts cmd in its own process group and makes
// context cancellation kill the WHOLE group instead of just the leader.
//
// exec.CommandContext's default Cancel sends SIGKILL to the leader only — a
// test/build runner that spawns children (`npm test` → node, `go test` → the
// compiled test binary, an arbitrary delta-check command) leaves those
// children ORPHANED on timeout, silently burning CPU forever (v0.100.104:
// the same class as the bash orphaned-`yes` leak, on the runner paths).
//
// Call it AFTER exec.CommandContext and BEFORE Start/Run.
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
