package git

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeInfo describes a git worktree.
type WorktreeInfo struct {
	Path           string // Absolute path to worktree root
	Branch         string // Branch name or "(detached)" for detached HEAD
	Head           string // SHA of HEAD commit
	IsMainWorktree bool   // True if this is the main repository
	IsCurrent      bool   // True if this worktree contains the current working directory
	IsBare         bool   // True if this is a bare worktree entry
}

// DetectWorktree checks if the given directory is inside a git worktree.
// Returns the WorktreeInfo if inside a worktree, or nil if in the main repo or not a git repo.
func DetectWorktree(workDir string) *WorktreeInfo {
	// Check if this is a worktree by looking at .git file (worktrees have a .git FILE, not directory)
	gitPath := filepath.Join(workDir, ".git")

	// Get the real git common dir (shared between main repo and worktrees)
	commonDir, err := exec.Command("git", "-C", workDir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return nil
	}
	gitDir, err := exec.Command("git", "-C", workDir, "rev-parse", "--git-dir").Output()
	if err != nil {
		return nil
	}

	commonDirStr := strings.TrimSpace(string(commonDir))
	gitDirStr := strings.TrimSpace(string(gitDir))

	// If git-dir != git-common-dir, we're in a worktree
	absCommon, _ := filepath.Abs(filepath.Join(workDir, commonDirStr))
	absGitDir, _ := filepath.Abs(filepath.Join(workDir, gitDirStr))

	isWorktree := absCommon != absGitDir

	_ = gitPath // unused but documents intent

	branch := GetCurrentBranch(workDir)
	head := getHead(workDir)

	return &WorktreeInfo{
		Path:           workDir,
		Branch:         branch,
		Head:           head,
		IsMainWorktree: !isWorktree,
		IsCurrent:      true,
	}
}

// ListWorktrees returns all worktrees in the repository.
func ListWorktrees(workDir string) []WorktreeInfo {
	out, err := exec.Command("git", "-C", workDir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}

	var trees []WorktreeInfo
	var current WorktreeInfo

	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			if current.Path != "" {
				trees = append(trees, current)
			}
			current = WorktreeInfo{}
			continue
		}

		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			current.Path = after
		} else if after, ok := strings.CutPrefix(line, "HEAD "); ok {
			current.Head = after
		} else if ref, ok := strings.CutPrefix(line, "branch "); ok {
			// Strip refs/heads/ prefix
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		} else if line == "bare" {
			current.IsBare = true
		} else if line == "detached" {
			current.Branch = "(detached)"
		}
	}

	// Flush last entry
	if current.Path != "" {
		trees = append(trees, current)
	}

	// Mark which is current
	absWorkDir, _ := filepath.Abs(workDir)
	for i := range trees {
		absPath, _ := filepath.Abs(trees[i].Path)
		if absPath == absWorkDir {
			trees[i].IsCurrent = true
		}
		if i == 0 {
			trees[i].IsMainWorktree = true
		}
	}

	return trees
}

// GetMainWorktreeRoot returns the root of the main repository (not the worktree).
// Returns workDir if not in a worktree or if detection fails.
func GetMainWorktreeRoot(workDir string) string {
	out, err := exec.Command("git", "-C", workDir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return workDir
	}

	commonDir := strings.TrimSpace(string(out))
	if commonDir == ".git" || commonDir == "" {
		return workDir
	}

	// Common dir is absolute or relative — resolve it
	absCommon, err := filepath.Abs(filepath.Join(workDir, commonDir))
	if err != nil {
		return workDir
	}

	// The main repo root is the parent of the common dir (which is .git/)
	return filepath.Dir(absCommon)
}

func getHead(workDir string) string {
	out, err := exec.Command("git", "-C", workDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
