//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcAttrForGroup puts the hook's child process in its own process
// group (Setpgid) so killProcessGroup can terminate the WHOLE group —
// including any backgrounded/detached children the hook command spawns
// (e.g. `sleep 9999 &`) — not just the immediate `sh -c` shell. Without
// this, a hook that backgrounds a child hangs the synchronous
// PreToolUse/turn well past the configured timeout: cmd.Wait() blocks
// until every holder of the stdout/stderr pipe's write end closes it
// (inherited by the backgrounded child, which never exits), so killing
// only the immediate shell process never unblocks it.
func setProcAttrForGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup signals the WHOLE process group (negative PID) the hook
// command was started in, so backgrounded/detached children die too, not
// just the immediate shell.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}
