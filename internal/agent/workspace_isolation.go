package agent

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gokin/internal/git"
	"gokin/internal/logging"
)

type workspaceIsolationMode string

const (
	workspaceIsolationDisabled  workspaceIsolationMode = ""
	workspaceIsolationReadOnly  workspaceIsolationMode = "read_only"
	workspaceIsolationApplyBack workspaceIsolationMode = "apply_back"
)

type isolatedWorkspaceApplyBackResult struct {
	Mode         string
	ChangedFiles []string
	PatchBytes   int
}

type WorkspaceChangePreview struct {
	FilePath   string
	OldContent string
	NewContent string
	IsNewFile  bool
}

type isolatedWorkspace struct {
	Root               string
	BaseRoot           string
	Strategy           string
	ApplyBackOnSuccess bool
	cleanup            func() error
}

func (w *isolatedWorkspace) Cleanup() error {
	if w == nil || w.cleanup == nil {
		return nil
	}
	return w.cleanup()
}

func (w *isolatedWorkspace) ApplyBack() (*isolatedWorkspaceApplyBackResult, error) {
	if w == nil || !w.ApplyBackOnSuccess {
		return nil, nil
	}
	if strings.TrimSpace(w.BaseRoot) == "" {
		return nil, fmt.Errorf("isolated workspace is missing base root")
	}

	switch w.Strategy {
	case "git_worktree":
		return applyGitWorkspaceChanges(w.BaseRoot, w.Root)
	default:
		return nil, fmt.Errorf("apply-back not supported for %q strategy", w.Strategy)
	}
}

func (w *isolatedWorkspace) ChangePreviews() ([]WorkspaceChangePreview, error) {
	if w == nil {
		return nil, nil
	}
	if strings.TrimSpace(w.BaseRoot) == "" {
		return nil, fmt.Errorf("isolated workspace is missing base root")
	}

	switch w.Strategy {
	case "git_worktree":
		return collectGitWorkspaceChangePreviews(w.BaseRoot, w.Root)
	default:
		return nil, fmt.Errorf("change previews not supported for %q strategy", w.Strategy)
	}
}

func prepareIsolatedWorkspace(baseWorkDir string, mode workspaceIsolationMode) (*isolatedWorkspace, error) {
	root := strings.TrimSpace(baseWorkDir)
	if root == "" {
		return nil, fmt.Errorf("workDir is empty")
	}

	switch mode {
	case workspaceIsolationReadOnly:
		return prepareReadOnlyIsolatedWorkspace(root)
	case workspaceIsolationApplyBack:
		return prepareApplyBackWorkspace(root)
	default:
		return nil, fmt.Errorf("unsupported workspace isolation mode %q", mode)
	}
}

func prepareReadOnlyIsolatedWorkspace(root string) (*isolatedWorkspace, error) {
	if git.IsGitRepo(root) {
		ws, err := prepareGitWorktree(root, false)
		if err == nil {
			return ws, nil
		}
		logging.Debug("failed to create git worktree, falling back to copy", "workdir", root, "error", err)
	}

	return prepareCopiedWorkspace(root)
}

func prepareApplyBackWorkspace(root string) (*isolatedWorkspace, error) {
	if !git.IsGitRepo(root) {
		return nil, fmt.Errorf("apply-back workspace isolation requires a git repository")
	}
	return prepareGitWorktree(root, true)
}

func prepareGitWorktree(baseWorkDir string, applyBackOnSuccess bool) (*isolatedWorkspace, error) {
	parent, err := os.MkdirTemp("", "gokin-agent-worktree-*")
	if err != nil {
		return nil, err
	}

	worktreeDir := filepath.Join(parent, "workspace")
	cmd := exec.Command("git", "-C", baseWorkDir, "worktree", "add", "--detach", worktreeDir, "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(parent)
		return nil, fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(output)))
	}

	return &isolatedWorkspace{
		Root:               worktreeDir,
		BaseRoot:           baseWorkDir,
		Strategy:           "git_worktree",
		ApplyBackOnSuccess: applyBackOnSuccess,
		cleanup: func() error {
			cmd := exec.Command("git", "-C", baseWorkDir, "worktree", "remove", "--force", worktreeDir)
			output, err := cmd.CombinedOutput()
			removeErr := os.RemoveAll(parent)
			if err != nil {
				if removeErr != nil {
					return fmt.Errorf("git worktree remove failed: %s; cleanup failed: %w", strings.TrimSpace(string(output)), removeErr)
				}
				return fmt.Errorf("git worktree remove failed: %s", strings.TrimSpace(string(output)))
			}
			return removeErr
		},
	}, nil
}

func prepareCopiedWorkspace(baseWorkDir string) (*isolatedWorkspace, error) {
	isolatedDir, err := os.MkdirTemp("", "gokin-agent-workspace-*")
	if err != nil {
		return nil, err
	}

	if err := copyWorkspaceTree(baseWorkDir, isolatedDir); err != nil {
		_ = os.RemoveAll(isolatedDir)
		return nil, err
	}

	return &isolatedWorkspace{
		Root:     isolatedDir,
		BaseRoot: baseWorkDir,
		Strategy: "copy",
		cleanup: func() error {
			return os.RemoveAll(isolatedDir)
		},
	}, nil
}

func applyGitWorkspaceChanges(baseWorkDir, isolatedWorkDir string) (*isolatedWorkspaceApplyBackResult, error) {
	if !git.IsGitRepo(baseWorkDir) {
		return nil, fmt.Errorf("base workspace is not a git repository")
	}
	if !git.IsGitRepo(isolatedWorkDir) {
		return nil, fmt.Errorf("isolated workspace is not a git repository")
	}

	if _, err := runGitCommand(isolatedWorkDir, "add", "-N", "--all"); err != nil {
		return nil, fmt.Errorf("failed to stage isolated workspace paths: %w", err)
	}

	changedFilesOut, err := runGitCommand(isolatedWorkDir, "diff", "--name-only", "--no-ext-diff", "--no-renames", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("failed to list isolated workspace changes: %w", err)
	}
	changedFiles := parseGitPathList(changedFilesOut)
	if len(changedFiles) == 0 {
		return &isolatedWorkspaceApplyBackResult{}, nil
	}

	patch, err := runGitCommand(isolatedWorkDir, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-renames", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("failed to build isolated workspace patch: %w", err)
	}
	if len(patch) == 0 {
		return &isolatedWorkspaceApplyBackResult{ChangedFiles: changedFiles}, nil
	}

	applyMode, err := selectGitApplyMode(baseWorkDir, patch)
	if err != nil {
		return nil, err
	}
	if err := gitApplyPatch(baseWorkDir, patch, applyMode, false); err != nil {
		return nil, fmt.Errorf("failed to apply isolated workspace patch: %w", err)
	}

	return &isolatedWorkspaceApplyBackResult{
		Mode:         applyMode,
		ChangedFiles: changedFiles,
		PatchBytes:   len(patch),
	}, nil
}

func collectGitWorkspaceChangePreviews(baseWorkDir, isolatedWorkDir string) ([]WorkspaceChangePreview, error) {
	if _, err := runGitCommand(isolatedWorkDir, "add", "-N", "--all"); err != nil {
		return nil, fmt.Errorf("failed to stage isolated workspace paths for review: %w", err)
	}

	changedFilesOut, err := runGitCommand(isolatedWorkDir, "diff", "--name-only", "--no-ext-diff", "--no-renames", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("failed to list isolated workspace changes: %w", err)
	}
	changedFiles := parseGitPathList(changedFilesOut)
	if len(changedFiles) == 0 {
		return nil, nil
	}

	previews := make([]WorkspaceChangePreview, 0, len(changedFiles))
	for _, relPath := range changedFiles {
		basePath := filepath.Join(baseWorkDir, filepath.FromSlash(relPath))
		isolatedPath := filepath.Join(isolatedWorkDir, filepath.FromSlash(relPath))

		oldContent, oldExists, err := readWorkspacePreviewFile(basePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read base file %s: %w", relPath, err)
		}
		newContent, newExists, err := readWorkspacePreviewFile(isolatedPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read isolated file %s: %w", relPath, err)
		}
		if !oldExists && !newExists {
			continue
		}

		previews = append(previews, WorkspaceChangePreview{
			FilePath:   relPath,
			OldContent: oldContent,
			NewContent: newContent,
			IsNewFile:  !oldExists && newExists,
		})
	}

	return previews, nil
}

func selectGitApplyMode(baseWorkDir string, patch []byte) (string, error) {
	if err := gitApplyPatch(baseWorkDir, patch, "3way", true); err == nil {
		return "3way", nil
	} else if directErr := gitApplyPatch(baseWorkDir, patch, "direct", true); directErr == nil {
		return "direct", nil
	} else {
		return "", fmt.Errorf("git apply check failed (3way: %v; direct: %v)", err, directErr)
	}
}

func gitApplyPatch(workDir string, patch []byte, mode string, check bool) error {
	args := []string{"apply", "--whitespace=nowarn"}
	if check {
		args = append(args, "--check")
	}
	switch mode {
	case "3way":
		args = append(args, "--3way")
	case "direct":
	default:
		return fmt.Errorf("unsupported git apply mode %q", mode)
	}

	_, err := runGitCommandWithInput(workDir, patch, args...)
	return err
}

func runGitCommand(workDir string, args ...string) ([]byte, error) {
	return runGitCommandWithInput(workDir, nil, args...)
}

func runGitCommandWithInput(workDir string, input []byte, args ...string) ([]byte, error) {
	cmdArgs := append([]string{"-C", workDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	if len(input) > 0 {
		cmd.Stdin = bytes.NewReader(input)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, formatGitCommandError(args, output, err)
	}
	return output, nil
}

func formatGitCommandError(args []string, output []byte, err error) error {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("git %s failed: %s", strings.Join(args, " "), trimmed)
}

func parseGitPathList(output []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	paths := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		paths = append(paths, line)
	}
	return paths
}

func readWorkspacePreviewFile(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}

	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return "[binary content omitted]\n", true, nil
	}

	return string(data), true, nil
}

func copyWorkspaceTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		targetPath := filepath.Join(dstRoot, relPath)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}

		switch mode := info.Mode(); {
		case mode.IsDir():
			return os.MkdirAll(targetPath, mode.Perm())
		case mode&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			// Validate symlink target is within srcRoot to prevent escape from
			// isolated workspace. An external symlink (../../etc/passwd) would
			// let the agent read/write outside the sandbox.
			absTarget := linkTarget
			if !filepath.IsAbs(linkTarget) {
				absTarget = filepath.Join(filepath.Dir(path), linkTarget)
			}
			absTarget = filepath.Clean(absTarget)
			if !strings.HasPrefix(absTarget, srcRoot+string(filepath.Separator)) && absTarget != srcRoot {
				logging.Debug("skipping external symlink in workspace copy",
					"link", path, "target", linkTarget)
				return nil // skip — don't copy symlinks that escape the workspace
			}
			return os.Symlink(linkTarget, targetPath)
		case mode.IsRegular():
			return copyRegularFile(path, targetPath, mode.Perm())
		default:
			return nil
		}
	})
}

func copyRegularFile(srcPath, dstPath string, perm os.FileMode) error {
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		_ = dstFile.Close()
		return err
	}

	return dstFile.Close()
}
