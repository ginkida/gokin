//go:build windows

package mcp

import "os/exec"

func configureServerProcessGroup(_ *exec.Cmd) {}

func killServerProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
