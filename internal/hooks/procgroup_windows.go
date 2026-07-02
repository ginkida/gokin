//go:build windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcAttrForGroup is a no-op on Windows — Setpgid-based process groups
// are POSIX-only. A hook that backgrounds a detached child on Windows can
// still outlive the timeout (a pre-existing, documented Windows limitation);
// this keeps the same single-process behavior this platform already had.
func setProcAttrForGroup(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing just the immediate process on
// Windows — process-group termination would require Job Objects, out of
// scope for this fix. Windows already has degraded signal semantics here:
// os.Process.Signal doesn't deliver SIGTERM on Windows, so the caller's
// SIGTERM attempt fails and it goes straight to Kill, same as before.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if sig == syscall.SIGKILL {
		return cmd.Process.Kill()
	}
	return cmd.Process.Signal(sig)
}
