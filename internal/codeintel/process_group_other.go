//go:build !unix

package codeintel

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}

func killProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
