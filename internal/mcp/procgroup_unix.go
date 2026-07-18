//go:build unix

package mcp

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureServerProcessGroup puts the stdio MCP server in its own process
// group so the shutdown kill can take its DESCENDANTS too — `npx some-server`
// spawns node children that a leader-only Kill orphans forever (v0.100.104:
// the orphaned-`yes` class, MCP flavor).
func configureServerProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killServerProcessTree kills the server's whole process group; falls back to
// the leader if group setup was rejected by the host.
func killServerProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = cmd.Process.Kill()
	}
}
