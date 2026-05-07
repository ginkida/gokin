package commands

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"gokin/internal/tools"
)

// CommitCommand creates a git commit.
type CommitCommand struct{}

func (c *CommitCommand) Name() string        { return "commit" }
func (c *CommitCommand) Description() string { return "Create a git commit" }
func (c *CommitCommand) Usage() string       { return "/commit [-m message]" }
func (c *CommitCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "commit",
		Priority:    10,
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "-m \"msg\"",
	}
}

func (c *CommitCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()

	// Check if we're in a git repository
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	// Parse arguments
	var message string
	for i := 0; i < len(args); i++ {
		if args[i] == "-m" && i+1 < len(args) {
			message = args[i+1]
			i++
		}
	}

	// Get git status
	status, err := runGitCommandCtx(ctx, workDir, "status", "--porcelain")
	if err != nil {
		return fmt.Sprintf("Failed to get git status: %v", err), nil
	}

	if strings.TrimSpace(status) == "" {
		return "No changes to commit.", nil
	}

	// Show what will be committed
	var result strings.Builder
	result.WriteString("Changes to commit:\n")

	// Get staged changes
	staged, _ := runGitCommandCtx(ctx, workDir, "diff", "--cached", "--stat")
	if staged != "" {
		result.WriteString("\nStaged:\n")
		result.WriteString(staged)
	}

	// Get unstaged changes
	unstaged, _ := runGitCommandCtx(ctx, workDir, "diff", "--stat")
	if unstaged != "" {
		result.WriteString("\nUnstaged:\n")
		result.WriteString(unstaged)
	}

	// Get untracked files
	untracked := getUntrackedFiles(status)
	if len(untracked) > 0 {
		result.WriteString("\nUntracked files:\n")
		for _, f := range untracked {
			fmt.Fprintf(&result, "  %s\n", f)
		}
	}

	// Edge case: only untracked files (no staged, no unstaged-tracked).
	// The flow below would call `git add -u` which stages nothing for
	// untracked files, then `git commit` would fail with the cryptic
	// "nothing to commit, working tree clean" error. Catch it early
	// with an actionable message.
	if staged == "" && unstaged == "" && len(untracked) > 0 {
		result.WriteString("\n")
		result.WriteString("Only untracked files — /commit auto-stages tracked changes only (`git add -u`).\n")
		result.WriteString("To include these, run `git add <file>` first, or `git add -A` to add everything.")
		return result.String(), nil
	}

	// Auto-generate commit message if not provided
	if message == "" {
		generated := autoGenerateCommitMessage(ctx, workDir)
		if generated != "" {
			message = generated
			fmt.Fprintf(&result, "\nAuto-generated message: %s\n", message)
		} else {
			// Fallback if auto-generation fails
			log, _ := runGitCommandCtx(ctx, workDir, "log", "-3", "--oneline")
			if log != "" {
				result.WriteString("\nRecent commits (for style reference):\n")
				result.WriteString(log)
			}
			result.WriteString("\nUse /commit -m \"your message\" to commit these changes.")
			return result.String(), nil
		}
	}

	// Stage only tracked (modified/deleted) files — safe default
	_, err = runGitCommandCtx(ctx, workDir, "add", "-u")
	if err != nil {
		return fmt.Sprintf("Failed to stage changes: %v", err), nil
	}

	// Warn about untracked files that won't be committed
	if len(untracked) > 0 {
		fmt.Fprintf(&result, "\nNote: %d untracked file(s) not staged (use git add manually):\n", len(untracked))
		for _, f := range untracked {
			fmt.Fprintf(&result, "  %s\n", f)
		}
	}

	// Create commit
	_, err = runGitCommandCtx(ctx, workDir, "commit", "-m", message)
	if err != nil {
		return fmt.Sprintf("Failed to create commit: %v", err), nil
	}

	// Get the commit hash
	hash, _ := runGitCommandCtx(ctx, workDir, "rev-parse", "--short", "HEAD")
	hash = strings.TrimSpace(hash)

	// Count files changed
	commitStat, _ := runGitCommandCtx(ctx, workDir, "diff", "--stat", "HEAD~1..HEAD")
	fileCount := strings.Count(commitStat, "|")

	msg := fmt.Sprintf("✓ Committed %s: %s", hash, message)
	if fileCount > 0 {
		msg += fmt.Sprintf(" (%d file(s))", fileCount)
	}
	return msg, nil
}

// PRCommand creates a pull request.
type PRCommand struct{}

func (c *PRCommand) Name() string        { return "pr" }
func (c *PRCommand) Description() string { return "Create a pull request" }
func (c *PRCommand) Usage() string       { return "/pr [--title title] [--draft] [--base branch]" }
func (c *PRCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:         CategoryGit,
		Icon:             "pr",
		Priority:         20,
		RequiresGit:      true,
		HasArgs:          true,
		ArgHint:          "--title \"...\"",
		LongRunning:      true,
		LongRunningLabel: "Creating PR — gh API call (push if needed, then open PR)...",
	}
}

func (c *PRCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()

	// Check if we're in a git repository
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	// Check if gh CLI is available
	if !isGHAvailable() {
		return "GitHub CLI (gh) is not installed. Install it from https://cli.github.com/", nil
	}

	// Parse arguments
	var title, base string
	var draft bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title", "-t":
			if i+1 < len(args) {
				title = args[i+1]
				i++
			}
		case "--base", "-b":
			if i+1 < len(args) {
				base = args[i+1]
				i++
			}
		case "--draft", "-d":
			draft = true
		}
	}

	// Get current branch
	currentBranch, err := runGitCommandCtx(ctx, workDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Sprintf("Failed to get current branch: %v", err), nil
	}
	currentBranch = strings.TrimSpace(currentBranch)

	// Check if we're on main/master
	if currentBranch == "main" || currentBranch == "master" {
		return fmt.Sprintf("Cannot create PR from %s branch. Create a feature branch first.", currentBranch), nil
	}

	// Determine base branch
	if base == "" {
		base = detectBaseBranch(ctx, workDir)
	}

	// Get commits for this branch
	commits, _ := runGitCommandCtx(ctx, workDir, "log", fmt.Sprintf("%s..HEAD", base), "--oneline")
	if strings.TrimSpace(commits) == "" {
		return fmt.Sprintf("No commits to create PR. Branch is up to date with %s.", base), nil
	}

	var result strings.Builder
	fmt.Fprintf(&result, "Branch: %s -> %s\n\n", currentBranch, base)
	result.WriteString("Commits:\n")
	result.WriteString(commits)
	result.WriteString("\n")

	// Check if there are unpushed commits
	unpushed, _ := runGitCommandCtx(ctx, workDir, "log", "@{upstream}..HEAD", "--oneline")
	if strings.TrimSpace(unpushed) != "" {
		result.WriteString("\nNote: You have unpushed commits. Push them first with: git push -u origin " + currentBranch)
		return result.String(), nil
	}

	// If no title, show info and ask for title
	if title == "" {
		result.WriteString("\nUse /pr --title \"Your PR title\" to create the pull request.")
		return result.String(), nil
	}

	// Build gh pr create command
	ghArgs := []string{"pr", "create", "--title", title, "--base", base}
	if draft {
		ghArgs = append(ghArgs, "--draft")
	}

	// Generate body from commits
	body := fmt.Sprintf("## Summary\n\nThis PR includes the following changes:\n\n%s", formatCommitsAsMarkdown(commits))
	ghArgs = append(ghArgs, "--body", body)

	// Create PR — gh hits the GitHub API and can sit for 5–10s on flaky
	// networks; ctx-aware so Esc actually kills the call.
	output, err := runCommandCtx(ctx, workDir, "gh", ghArgs...)
	if err != nil {
		return fmt.Sprintf("Failed to create PR: %v\n%s", err, output), nil
	}

	return fmt.Sprintf("Pull request created: %s", strings.TrimSpace(output)), nil
}

// Helper functions

func isGitRepo(dir string) bool {
	_, err := runGitCommand(dir, "rev-parse", "--git-dir")
	return err == nil
}

func isGHAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

func runGitCommand(dir string, args ...string) (string, error) {
	return runCommand(dir, "git", args...)
}

// runGitCommandCtx is the cancellable form. Pass the request context so the
// user pressing Esc actually kills the underlying git process — important on
// huge repos / locked refs where git can sit for tens of seconds.
func runGitCommandCtx(ctx context.Context, dir string, args ...string) (string, error) {
	return runCommandCtx(ctx, dir, "git", args...)
}

func runCommand(dir, name string, args ...string) (string, error) {
	return runCommandCtx(context.Background(), dir, name, args...)
}

// runCommandCtx runs name+args in dir with cancellation tied to ctx. Cancelling
// ctx terminates the process via os.Kill (exec.CommandContext default).
func runCommandCtx(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stderr.String(), err
	}

	return stdout.String(), nil
}

func getUntrackedFiles(status string) []string {
	var files []string
	lines := strings.Split(status, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "??") {
			file := strings.TrimPrefix(line, "?? ")
			files = append(files, file)
		}
	}
	return files
}

func detectBaseBranch(ctx context.Context, dir string) string {
	// Check if main exists
	_, err := runGitCommandCtx(ctx, dir, "rev-parse", "--verify", "main")
	if err == nil {
		return "main"
	}

	// Fall back to master
	_, err = runGitCommandCtx(ctx, dir, "rev-parse", "--verify", "master")
	if err == nil {
		return "master"
	}

	// Default to main
	return "main"
}

// autoGenerateCommitMessage analyzes staged changes and generates a conventional
// commit message (e.g., "feat(ui): add multi-line input support").
func autoGenerateCommitMessage(ctx context.Context, workDir string) string {
	// Get staged diff stats
	stat, err := runGitCommandCtx(ctx, workDir, "diff", "--cached", "--stat")
	if err != nil || strings.TrimSpace(stat) == "" {
		return ""
	}

	// Get detailed diff (limited for analysis); error is intentionally ignored —
	// the auto-generated message still uses stat output even without the full diff.
	diff, _ := runGitCommandCtx(ctx, workDir, "diff", "--cached")
	if runes := []rune(diff); len(runes) > 4000 {
		diff = string(runes[:4000])
	}

	// Parse file list and change counts from stat
	var files []string
	var totalAdded, totalRemoved int
	for line := range strings.SplitSeq(stat, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, "files changed") || strings.Contains(line, "file changed") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if (p == "insertions(+)" || p == "insertion(+)") && i > 0 {
					fmt.Sscanf(parts[i-1], "%d", &totalAdded)
				}
				if (p == "deletions(-)" || p == "deletion(-)") && i > 0 {
					fmt.Sscanf(parts[i-1], "%d", &totalRemoved)
				}
			}
			continue
		}
		if idx := strings.Index(line, "|"); idx > 0 {
			files = append(files, strings.TrimSpace(line[:idx]))
		}
	}

	if len(files) == 0 {
		return ""
	}

	changeType := tools.DetectChangeType(files, diff)
	scope := tools.DetectScope(files)
	desc := tools.GenerateDescription(files, totalAdded, totalRemoved, diff)

	var msg strings.Builder
	msg.WriteString(changeType)
	if scope != "" {
		msg.WriteString("(" + scope + ")")
	}
	msg.WriteString(": ")
	msg.WriteString(desc)

	return msg.String()
}

func formatCommitsAsMarkdown(commits string) string {
	var result strings.Builder
	lines := strings.Split(strings.TrimSpace(commits), "\n")
	for _, line := range lines {
		if line != "" {
			fmt.Fprintf(&result, "- %s\n", line)
		}
	}
	return result.String()
}
