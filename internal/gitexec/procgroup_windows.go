//go:build windows

package gitexec

import "os/exec"

// KillProcessGroupOnCancel retains CommandContext's leader kill on Windows,
// matching tools.KillProcessGroupOnCancel on that platform.
func KillProcessGroupOnCancel(_ *exec.Cmd) {}
