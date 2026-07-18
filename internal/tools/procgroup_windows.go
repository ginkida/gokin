//go:build windows

package tools

import "os/exec"

// KillProcessGroupOnCancel is a best-effort no-op on Windows: CommandContext's
// default leader Kill applies. Descendant cleanup on cancellation is covered
// where it matters most (the bash tool's taskkill /T path).
func KillProcessGroupOnCancel(_ *exec.Cmd) {}
