//go:build !unix

package git

import "os/exec"

func killProbeGroupOnCancel(cmd *exec.Cmd) {}
