package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// LogCommand prints recent git commits inline. The "what did I just
// do?" command — pairs with /diff (pending changes) and /commit (new
// commits) to give the user a complete inspection toolkit without
// leaving the TUI or asking the agent to run shell.
//
// Defaults to the last 10 commits with one-line summaries. Pass an
// integer for a different count, or a file path to scope to commits
// that touched that file.
type LogCommand struct{}

func (c *LogCommand) Name() string        { return "log" }
func (c *LogCommand) Description() string { return "Show recent git commits" }
func (c *LogCommand) Usage() string {
	return `/log              - Last 10 commits
/log 20           - Last 20 commits (max 100)
/log <file>       - Commits touching <file>
/log 5 <file>     - Last 5 commits touching <file>`
}

func (c *LogCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "log",
		Priority:    12, // sits next to /commit (10) + /diff (11)
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "[count] [file]",
	}
}

const (
	logDefaultCount = 10
	logMaxCount     = 100
)

func (c *LogCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	// Parse: a single integer arg sets count; anything else is treated
	// as a file path. Order is permissive — `/log 5 main.go`,
	// `/log main.go`, `/log 5` all work.
	count := logDefaultCount
	var filePath string
	for _, a := range args {
		if a == "" {
			continue
		}
		if n, err := strconv.Atoi(a); err == nil {
			if n < 1 {
				return fmt.Sprintf("Invalid count %q — pass a positive integer.", a), nil
			}
			if n > logMaxCount {
				n = logMaxCount
			}
			count = n
			continue
		}
		filePath = a
	}

	// Compose git log args. The pretty format is custom rather than
	// --oneline so we can keep the hash short, color-friendly subject
	// line, and a relative-time hint that's much more readable than
	// the absolute date most `git log` flags emit.
	gitArgs := []string{
		"log",
		fmt.Sprintf("-%d", count),
		"--pretty=format:%h · %ar · %an · %s",
		"--no-color",
	}
	if filePath != "" {
		gitArgs = append(gitArgs, "--", filePath)
	}

	out, err := runGitCommandCtx(ctx, workDir, gitArgs...)
	if err != nil {
		return fmt.Sprintf("Failed to read git log: %v", err), nil
	}
	out = strings.TrimRight(out, "\n")

	if out == "" {
		if filePath != "" {
			return fmt.Sprintf("No commits touching %s yet.", filePath), nil
		}
		return "No commits yet (empty repository).", nil
	}

	scope := fmt.Sprintf("last %d", count)
	if filePath != "" {
		scope = fmt.Sprintf("last %d touching %s", count, filePath)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Commits (%s):\n\n", scope))
	for line := range strings.SplitSeq(out, "\n") {
		sb.WriteString("  ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// Compile-time check.
var _ Command = (*LogCommand)(nil)
