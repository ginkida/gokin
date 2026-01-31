//go:build !linux && unix

package security

import (
	"syscall"
)

// applySandbox applies basic process isolation for non-Linux Unix platforms (macOS, BSD)
func (sc *SandboxedCommand) applySandbox(workDir string) error {
	// For non-Linux Unix systems, we provide basic process group isolation
	sc.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	return nil
}
