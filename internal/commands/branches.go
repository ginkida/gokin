package commands

import (
	"context"
	"fmt"
	"strings"
)

// BranchesCommand lists local branches with their last-commit subject
// and a "* current" marker. Completes the git-inspection set:
//
//	/log       — what's already committed (chronologically)
//	/diff      — what's about to commit (working tree)
//	/branches  — what other lines of work exist (branch list)
//	/commit    — the write
//
// Used to require a shell-out (`git branch -v`) or asking the agent.
// Now: instant, deterministic, zero-token.
type BranchesCommand struct{}

func (c *BranchesCommand) Name() string        { return "branches" }
func (c *BranchesCommand) Description() string { return "List local branches with last commit" }
func (c *BranchesCommand) Usage() string {
	return `/branches         - All local branches
/branches --all   - Include remote-tracking branches`
}

func (c *BranchesCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "branch",
		Priority:    13, // sits with /commit (10) /diff (11) /log (12)
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "[--all]",
	}
}

// branchesCap protects against very-long-running repos with hundreds
// of branches; the listing would otherwise dominate the TUI scrollback.
// 50 is enough to cover normal day-to-day work; the "more elided"
// footer points users at `git branch` for the full set.
const branchesCap = 50

func (c *BranchesCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	includeRemotes := false
	for _, a := range args {
		if a == "--all" || a == "-a" {
			includeRemotes = true
		}
	}

	// `git for-each-ref` lets us format hash + subject in one call,
	// avoiding the per-branch `git log -1` loop a naive impl would
	// require. The format string is intentionally simple and tab-
	// separated so we can split reliably without quoting tricks.
	refScope := "refs/heads"
	if includeRemotes {
		refScope = "refs/heads refs/remotes"
	}
	gitArgs := []string{
		"for-each-ref",
		"--sort=-committerdate", // newest activity first
		"--format=%(refname:short)\t%(objectname:short)\t%(committerdate:relative)\t%(subject)",
	}
	gitArgs = append(gitArgs, strings.Fields(refScope)...)

	out, err := runGitCommandCtx(ctx, workDir, gitArgs...)
	if err != nil {
		return fmt.Sprintf("Failed to list branches: %v", err), nil
	}
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return "No branches found (newly-initialized repo with no commits yet).", nil
	}

	// Find current branch — `git for-each-ref` doesn't mark it, we
	// need a separate cheap call. Detached HEAD returns empty, which
	// we render as "(detached)" for clarity.
	current, _ := runGitCommandCtx(ctx, workDir, "rev-parse", "--abbrev-ref", "HEAD")
	current = strings.TrimSpace(current)
	if current == "HEAD" {
		current = "" // detached
	}

	lines := strings.Split(out, "\n")
	if len(lines) > branchesCap {
		lines = lines[:branchesCap]
	}

	var sb strings.Builder
	scope := "local"
	if includeRemotes {
		scope = "local + remotes"
	}
	sb.WriteString(fmt.Sprintf("Branches (%s):\n\n", scope))

	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			// Defensive: row didn't have all four columns. Skip
			// silently rather than rendering a malformed line.
			continue
		}
		ref, hash, when, subject := parts[0], parts[1], parts[2], parts[3]

		marker := "  "
		if ref == current {
			marker = "* "
		}
		sb.WriteString(marker)
		sb.WriteString(ref)
		sb.WriteString("  ")
		sb.WriteString(hash)
		sb.WriteString("  ")
		sb.WriteString(when)
		sb.WriteString("  ")
		sb.WriteString(subject)
		sb.WriteString("\n")
	}

	if current == "" {
		sb.WriteString("\n_HEAD is detached — no current branch._\n")
	}

	if total := strings.Count(out, "\n") + 1; total > branchesCap {
		sb.WriteString(fmt.Sprintf("\n_Showing newest %d of %d. Run `git branch%s` for the full list._\n",
			branchesCap, total,
			func() string {
				if includeRemotes {
					return " --all"
				}
				return ""
			}()))
	}

	return sb.String(), nil
}

// Compile-time check.
var _ Command = (*BranchesCommand)(nil)
