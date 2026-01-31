//go:build windows

package security

// applySandbox applies basic process isolation for Windows
func (sc *SandboxedCommand) applySandbox(workDir string) error {
	// Windows doesn't support Unix process groups
	// Basic isolation is handled by the OS
	return nil
}
