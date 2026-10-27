package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// IsGitRepo is an optional context probe: errors, including timeout, mean false.
// Authority decisions such as workspace isolation must use CheckGitRepo.
func IsGitRepo(workDir string) bool {
	return isGitRepo(workDir, startupGitOutput)
}

func isGitRepo(workDir string, output gitOutput) bool {
	_, err := output(workDir, "rev-parse", "--git-dir")
	return err == nil
}

// CheckGitRepo is for authority decisions. Unlike the optional startup probe,
// cancellation and execution failures must never mean "not a repository".
func CheckGitRepo(ctx context.Context, workDir string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = workDir
	cmd.WaitDelay = 100 * time.Millisecond
	killProbeGroupOnCancel(cmd)
	_, err := cmd.Output()
	if ctx.Err() != nil {
		return false, fmt.Errorf("git repository probe: %w", ctx.Err())
	}
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "not a git repository") {
		return false, nil
	}
	return false, fmt.Errorf("git repository probe: %w", err)
}

// GetCurrentBranch returns the current git branch name.
// Returns empty string if not in a git repo or on error.
func GetCurrentBranch(workDir string) string {
	return getCurrentBranch(workDir, startupGitOutput)
}

func getCurrentBranch(workDir string, probe gitOutput) string {
	output, err := probe(workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
