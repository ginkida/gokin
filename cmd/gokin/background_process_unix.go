//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureDetachedProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func stopDetachedProcess(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		process, findErr := os.FindProcess(pid)
		if findErr != nil {
			return findErr
		}
		return process.Signal(syscall.SIGTERM)
	}
	return err
}
