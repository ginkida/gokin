package git

import (
	"context"
	"os/exec"
	"time"
)

// Startup probes are optional context, never a reason to wait indefinitely
// before a headless invocation's own timeout starts.
func startupGitOutput(workDir string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workDir
	cmd.WaitDelay = 100 * time.Millisecond
	killProbeGroupOnCancel(cmd)
	output, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output, err
}
