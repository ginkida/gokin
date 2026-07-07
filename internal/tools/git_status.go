package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"gokin/internal/security"

	"google.golang.org/genai"
)

// GitStatusTool shows git repository status.
type GitStatusTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

// NewGitStatusTool creates a new GitStatusTool instance.
func NewGitStatusTool(workDir string) *GitStatusTool {
	return &GitStatusTool{
		workDir:       workDir,
		pathValidator: security.NewPathValidator([]string{workDir}, false),
	}
}

func (t *GitStatusTool) Name() string {
	return "git_status"
}

func (t *GitStatusTool) Description() string {
	return "Shows the working tree status of a git repository."
}

func (t *GitStatusTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {
					Type:        genai.TypeString,
					Description: "Path to the repository (default: working directory)",
				},
				"short": {
					Type:        genai.TypeBoolean,
					Description: "If true, use short format output",
				},
			},
		},
	}
}

func (t *GitStatusTool) Validate(args map[string]any) error {
	// All parameters are optional
	return nil
}

func (t *GitStatusTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	path := GetStringDefault(args, "path", t.workDir)
	short := GetBoolDefault(args, "short", false)

	// Validate the model-supplied directory override — unlike every other
	// tool that takes a path, git_status assigned it straight to cmd.Dir
	// with no containment check at all, letting "path": "../../.." (a
	// relative override, which the executor's out-of-workspace gate only
	// checks for ABSOLUTE paths) run `git status` against an arbitrary
	// directory outside the granted workspace. Resolve relative overrides
	// against workDir first (matches list_dir.go's pattern) so a relative
	// escape can't be mistaken for "still inside" by filepath.Abs resolving
	// against the process's CWD instead.
	absPath := path
	if !filepath.IsAbs(path) {
		absPath = filepath.Join(t.workDir, path)
	}
	if t.pathValidator != nil {
		validPath, err := t.pathValidator.ValidateDir(absPath)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("path validation failed: %s", err)), nil
		}
		absPath = validPath
	}

	// ONE porcelain invocation with branch info covers everything: the
	// structured summary below names every file, so the old second
	// `git status` subprocess (full human output appended after the
	// summary) only duplicated content and doubled the cost per call.
	cmdArgs := []string{"status", "--porcelain", "-b"}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = absPath

	output, err := cmd.Output()
	if err != nil {
		// Error context must reach the model: cmd.Output() carries git's
		// stderr on the ExitError, not in the returned bytes — without this
		// a "not a git repository" or lock error degrades to "exit status 128".
		detail := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			detail = "\n" + strings.TrimSpace(string(exitErr.Stderr))
		}
		return NewErrorResult(fmt.Sprintf("git status failed: %s%s", err, detail)), nil
	}

	rawStatus := strings.TrimSpace(string(output))

	// First "-b" line carries branch/ahead-behind context: "## main...origin/main [ahead 2]"
	branch := ""
	var fileLines []string
	for _, line := range strings.Split(rawStatus, "\n") {
		if strings.HasPrefix(line, "## ") {
			branch = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if line != "" {
			fileLines = append(fileLines, line)
		}
	}

	if len(fileLines) == 0 {
		msg := "Nothing to commit, working tree clean."
		if branch != "" {
			msg += " On " + branch + "."
		}
		return NewSuccessResult(msg), nil
	}

	// Parse porcelain output
	var (
		staged    []string
		unstaged  []string
		untracked []string
		conflicts []string
	)
	for _, line := range fileLines {
		if len(line) < 3 {
			continue
		}
		status := line[:2] // XY
		file := line[3:]
		// Remove trailing whitespace from filename (rename targets have " -> ")
		file = strings.TrimSpace(file)

		x := status[0] // index (staging area)
		y := status[1] // working tree

		switch {
		case status == "??":
			untracked = append(untracked, file)
		case x == 'U' || y == 'U' || status == "AA" || status == "DD":
			conflicts = append(conflicts, file)
		case x != ' ' && x != '?':
			label := statusLabel(x) + " " + file
			staged = append(staged, label)
			if y != ' ' && y != '?' && y != x {
				// File has both staged and unstaged changes
				unstaged = append(unstaged, statusLabel(y)+" "+file+" (+ unstaged)")
			}
		case y != ' ' && y != '?':
			unstaged = append(unstaged, statusLabel(y)+" "+file)
		}
	}

	var builder strings.Builder

	// Summary header
	total := len(staged) + len(unstaged) + len(untracked) + len(conflicts)
	if branch != "" {
		fmt.Fprintf(&builder, "Working tree status on %s: %d change(s)", branch, total)
	} else {
		fmt.Fprintf(&builder, "Working tree status: %d change(s)", total)
	}
	parts := make([]string, 0, 4)
	if len(staged) > 0 {
		parts = append(parts, fmt.Sprintf("%d staged", len(staged)))
	}
	if len(unstaged) > 0 {
		parts = append(parts, fmt.Sprintf("%d unstaged", len(unstaged)))
	}
	if len(untracked) > 0 {
		parts = append(parts, fmt.Sprintf("%d untracked", len(untracked)))
	}
	if len(conflicts) > 0 {
		parts = append(parts, fmt.Sprintf("%d conflicts", len(conflicts)))
	}
	if len(parts) > 0 {
		builder.WriteString(" (" + strings.Join(parts, ", ") + ")")
	}
	builder.WriteString(":\n\n")

	// Actionable summary
	builder.WriteString(actionableGitStatusSummary(staged, unstaged, untracked, conflicts))

	// short=true appends the raw porcelain lines for machine-style reading.
	// The non-short path appends NOTHING extra: the structured summary above
	// already names every file, and the old full-`git status` tail merely
	// repeated it in prose (plus a second subprocess per call).
	if short {
		builder.WriteString("\nRaw:\n")
		builder.WriteString(strings.Join(fileLines, "\n"))
		builder.WriteString("\n")
	}

	return NewSuccessResult(builder.String()), nil
}

func statusLabel(code byte) string {
	switch code {
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'U':
		return "unmerged"
	case 'T':
		return "type-changed"
	default:
		return "changed"
	}
}

func actionableGitStatusSummary(staged, unstaged, untracked, conflicts []string) string {
	var b strings.Builder
	b.WriteString("Actionable summary:\n")

	// Conflicts take priority — must resolve before anything else
	if len(conflicts) > 0 {
		b.WriteString("- CONFLICTS: resolve these first before making other changes\n")
		for _, f := range conflicts {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
		b.WriteString("- Next: resolve conflicts, then git add the resolved files.\n")
		return b.String()
	}

	if len(staged) > 0 {
		b.WriteString("- Staged (ready to commit):\n")
		for _, f := range staged {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if len(unstaged) > 0 {
		b.WriteString("- Unstaged (modified but not staged):\n")
		for _, f := range unstaged {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	if len(untracked) > 0 {
		if len(untracked) <= 10 {
			fmt.Fprintf(&b, "- Untracked (%d): %s\n", len(untracked), strings.Join(untracked, ", "))
		} else {
			fmt.Fprintf(&b, "- Untracked (%d): %s, ...\n", len(untracked), strings.Join(untracked[:5], ", "))
		}
	}

	// Next hint
	b.WriteString("- Next: ")
	switch {
	case len(staged) > 0:
		b.WriteString("review staged changes (git diff --cached), then commit with a descriptive message.\n")
	case len(unstaged) > 0:
		b.WriteString("review unstaged changes (git diff), then stage (git add) and commit.\n")
	default:
		b.WriteString("add new files with git add, or continue working.\n")
	}
	b.WriteString("\n")
	return b.String()
}
