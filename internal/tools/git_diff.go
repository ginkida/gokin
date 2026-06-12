package tools

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/genai"
)

// GitDiffTool shows differences between commits, branches, or files.
type GitDiffTool struct {
	workDir string
}

// NewGitDiffTool creates a new GitDiffTool instance.
func NewGitDiffTool(workDir string) *GitDiffTool {
	return &GitDiffTool{
		workDir: workDir,
	}
}

func (t *GitDiffTool) Name() string {
	return "git_diff"
}

func (t *GitDiffTool) Description() string {
	return "Shows differences between commits, branches, or working tree. Can show full diff or just file names with status."
}

func (t *GitDiffTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.Name(),
		Description: t.Description(),
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"from": {
					Type:        genai.TypeString,
					Description: "Starting commit/branch (default: HEAD). Use empty string for unstaged changes.",
				},
				"to": {
					Type:        genai.TypeString,
					Description: "Ending commit/branch. Omit to compare with working tree.",
				},
				"name_status": {
					Type:        genai.TypeBoolean,
					Description: "Only show file names and status (A=added, M=modified, D=deleted)",
				},
				"file": {
					Type:        genai.TypeString,
					Description: "Show diff for a specific file only",
				},
				"staged": {
					Type:        genai.TypeBoolean,
					Description: "Show staged changes (--cached). Ignored if from/to are specified.",
				},
			},
		},
	}
}

func (t *GitDiffTool) Validate(args map[string]any) error {
	// All parameters are optional
	return nil
}

func (t *GitDiffTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	from := GetStringDefault(args, "from", "")
	to := GetStringDefault(args, "to", "")
	nameStatus := GetBoolDefault(args, "name_status", false)
	file := GetStringDefault(args, "file", "")
	staged := GetBoolDefault(args, "staged", false)

	// Build git diff command
	cmdArgs := []string{"diff"}

	if nameStatus {
		cmdArgs = append(cmdArgs, "--name-status")
	}

	// Handle different diff modes
	if from != "" && to != "" {
		if !isValidGitRef(from) || !isValidGitRef(to) {
			return NewErrorResult("invalid git ref: must not start with '-'"), nil
		}
		// Diff between two refs
		cmdArgs = append(cmdArgs, from+".."+to)
	} else if from != "" {
		if !isValidGitRef(from) {
			return NewErrorResult("invalid git ref: must not start with '-'"), nil
		}
		// Diff from a ref to working tree
		cmdArgs = append(cmdArgs, from)
	} else if staged {
		// Staged changes
		cmdArgs = append(cmdArgs, "--cached")
	}
	// else: unstaged changes (no additional args needed)

	if file != "" {
		// Make path relative to workDir if absolute
		if filepath.IsAbs(file) {
			if rel, err := filepath.Rel(t.workDir, file); err == nil {
				file = rel
			}
		}
		cmdArgs = append(cmdArgs, "--", file)
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = t.workDir

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return NewErrorResult(fmt.Sprintf("git diff failed: %s", stderr)), nil
			}
		}
		return NewErrorResult(fmt.Sprintf("git diff failed: %s", err)), nil
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return NewSuccessResult("No differences found."), nil
	}

	// Truncate if output is too large (rune-safe)
	const maxLen = 50000
	if runes := []rune(result); len(runes) > maxLen {
		result = string(runes[:maxLen]) + "\n\n... (diff truncated — use specific file paths to narrow the diff)"
	}

	// Build actionable summary for --name-status mode
	if nameStatus {
		var builder strings.Builder
		builder.WriteString(actionableGitDiffSummary(result))
		builder.WriteString("\n")
		builder.WriteString(result)
		return NewSuccessResult(builder.String()), nil
	}

	return NewSuccessResult(result), nil
}

// actionableGitDiffSummary parses --name-status diff output and provides a
// structured summary with change counts and "Next:" hint.
func actionableGitDiffSummary(raw string) string {
	if raw == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	var added, modified, deleted, renamed []string
	seen := map[string]bool{} // dedup same file appearing in multiple status lines

	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 2 {
			continue
		}
		status := strings.TrimSpace(parts[0])
		file := parts[len(parts)-1] // last column is always the file

		// Handle rename: "R100\tsrc\t\tdst" → use dst
		file = strings.TrimSpace(file)
		if len(parts) == 3 {
			// rename: status | old name | new name
			file = strings.TrimSpace(parts[2])
		}

		switch {
		case strings.HasPrefix(status, "A"):
			if !seen[file] {
				added = append(added, file)
				seen[file] = true
			}
		case strings.HasPrefix(status, "D"):
			if !seen[file] {
				deleted = append(deleted, file)
				seen[file] = true
			}
		case strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C"):
			if !seen[file] {
				renamed = append(renamed, file)
				seen[file] = true
			}
		default:
			if !seen[file] {
				modified = append(modified, file)
				seen[file] = true
			}
		}
	}

	var b strings.Builder
	b.WriteString("Actionable summary:\n")
	total := len(added) + len(modified) + len(deleted) + len(renamed)

	if total == 0 {
		b.WriteString("- No file-level changes detected.\n")
		b.WriteString("- Next: review the raw diff output above.\n")
		return b.String()
	}

	// Summary header
	parts := make([]string, 0, 4)
	if len(added) > 0 {
		parts = append(parts, fmt.Sprintf("%d added", len(added)))
	}
	if len(modified) > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", len(modified)))
	}
	if len(deleted) > 0 {
		parts = append(parts, fmt.Sprintf("%d deleted", len(deleted)))
	}
	if len(renamed) > 0 {
		parts = append(parts, fmt.Sprintf("%d renamed", len(renamed)))
	}
	b.WriteString(fmt.Sprintf("- %d file(s) changed (%s):\n", total, strings.Join(parts, ", ")))

	// Show top files per category
	printDiffCategory(&b, "  Added", added, 5)
	printDiffCategory(&b, "  Modified", modified, 5)
	printDiffCategory(&b, "  Deleted", deleted, 5)
	printDiffCategory(&b, "  Renamed", renamed, 5)

	// Next hint
	b.WriteString("- Next: ")
	switch {
	case len(added) > 0 || len(modified) > 0:
		b.WriteString("read changed files to understand the scope of changes, then act on the findings.\n")
	case len(deleted) > 0:
		b.WriteString("verify the deleted files are intentionally removed, then commit.\n")
	default:
		b.WriteString("review the diff, then commit or continue working.\n")
	}
	b.WriteString("\n")
	return b.String()
}

func printDiffCategory(b *strings.Builder, label string, files []string, limit int) {
	if len(files) == 0 {
		return
	}
	if len(files) <= limit {
		fmt.Fprintf(b, "%s: %s\n", label, strings.Join(files, ", "))
	} else {
		fmt.Fprintf(b, "%s (%d): %s, ...\n", label, len(files), strings.Join(files[:limit], ", "))
	}
}
