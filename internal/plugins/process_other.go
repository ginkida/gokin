//go:build !unix

package plugins

import "os/exec"

func configureProcess(cmd *exec.Cmd) {}
