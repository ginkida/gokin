package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"google.golang.org/genai"
)

// GitStatusTool shows git repository status.
type GitStatusTool struct {
	workDir string
}

// NewGitStatusTool creates a new GitStatusTool instance.
func NewGitStatusTool(workDir string) *GitStatusTool {
	return &GitStatusTool{
		workDir: workDir,
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

	// Always use short format for parsing, then build summary
	cmdArgs := []string{"status", "--porcelain"}
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = path

	output, err := cmd.Output()
	if err != nil {
		return NewErrorResult(fmt.Sprintf("git status failed: %s", err)), nil
	}

	rawStatus := strings.TrimSpace(string(output))
	if rawStatus == "" {
		return NewSuccessResult("Nothing to commit, working tree clean."), nil
	}

	// Parse porcelain output
	var (
		staged    []string
		unstaged  []string
		untracked []string
		conflicts []string
	)
	for _, line := range strings.Split(rawStatus, "\n") {
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
	fmt.Fprintf(&builder, "Working tree status: %d change(s)", total)
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

	// If short format requested or the user asked for raw, append the raw output
	if short {
		builder.WriteString("\nRaw:\n")
		builder.WriteString(rawStatus)
		builder.WriteString("\n")
	} else {
		// Append full (non-short) status for completeness
		fullCmd := exec.CommandContext(ctx, "git", "status")
		fullCmd.Dir = path
		if fullOut, fullErr := fullCmd.Output(); fullErr == nil {
			fullResult := strings.TrimSpace(string(fullOut))
			if runes := []rune(fullResult); len(runes) > 30000 {
				fullResult = string(runes[:30000]) + "\n\n... (status truncated, too many changes)"
			}
			builder.WriteString("\n")
			builder.WriteString(fullResult)
			builder.WriteString("\n")
		}
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
