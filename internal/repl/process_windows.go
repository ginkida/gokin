//go:build windows

package repl

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
