//go:build !linux && !darwin && !windows

package security

import (
	"fmt"
	"os/exec"
)

func wrapBashSandbox(_ *exec.Cmd, _ string, _ []sandboxMount, _ []string, _ string, _ bool) error {
	return fmt.Errorf("no bash isolation backend on this platform")
}
