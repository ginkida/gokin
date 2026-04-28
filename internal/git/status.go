package git

import (
	"os/exec"
	"strings"
)

// IsGitRepo checks if the working directory is a git repository.
func IsGitRepo(workDir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = workDir
	err := cmd.Run()
	return err == nil
}

// GetCurrentBranch returns the current git branch name.
// Returns empty string if not in a git repo or on error.
func GetCurrentBranch(workDir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workDir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
