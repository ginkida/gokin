package commands

import (
	"context"
	"fmt"
	"strings"
)

// DiffCommand shows pending git changes inline. Designed as the
// "what's about to commit" preview that pairs naturally with
// /commit — users hit /diff first to verify scope, then /commit
// when satisfied. Without this, the only way to see pending changes
// in-TUI was to ask the agent ("show me the diff") which costs an
// LLM round trip and is non-deterministic.
//
// Modes (mirrors `git diff` itself):
//
//	/diff             - Working tree (unstaged + staged) summary + content
//	/diff --stat      - Stats only (file list + +/- counts), no content
//	/diff --staged    - Staged-only content (git diff --cached)
//	/diff <file>      - Single-file diff
type DiffCommand struct{}

func (c *DiffCommand) Name() string        { return "diff" }
func (c *DiffCommand) Description() string { return "Show pending git changes" }
func (c *DiffCommand) Usage() string {
	return `/diff             - Show working-tree diff (with content)
/diff --stat      - Stats only (no content)
/diff --staged    - Staged changes only
/diff <file>      - Single file`
}

func (c *DiffCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "diff",
		Priority:    11, // sits right after /commit (which is 10)
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "[--stat | --staged | <file>]",
	}
}

// diffMaxOutputBytes caps the rendered diff so a sprawling refactor
// doesn't dump 50K lines into the TUI scrollback. The git command
// still ran fully — we just truncate the displayed text and append a
// "... truncated, run `git diff` directly for the full output" tail.
const diffMaxOutputBytes = 32 * 1024

func (c *DiffCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	statOnly := false
	staged := false
	var filePath string

	// Parse args. Positional file path is allowed alongside flags so
	// `/diff --staged path/to/file.go` works.
	for _, a := range args {
		switch a {
		case "--stat":
			statOnly = true
		case "--staged", "--cached":
			staged = true
		default:
			// Anything else is treated as a file path. Multiple paths
			// would be confusing in this surface — take the last one
			// silently (git accepts the same).
			filePath = a
		}
	}

	// Build git args.
	gitArgs := []string{"diff"}
	if staged {
		gitArgs = append(gitArgs, "--cached")
	}
	if statOnly {
		gitArgs = append(gitArgs, "--stat")
	} else {
		// Color is preserved in raw form; git's `--color` would inject
		// ANSI sequences but we render through the TUI's normal text
		// path which already styles diff blocks via diff_utils. Ask
		// for `--color=never` so we don't double-up.
		gitArgs = append(gitArgs, "--color=never")
	}
	if filePath != "" {
		gitArgs = append(gitArgs, "--", filePath)
	}

	out, err := runGitCommand(workDir, gitArgs...)
	if err != nil {
		return fmt.Sprintf("Failed to get diff: %v", err), nil
	}
	out = strings.TrimRight(out, "\n")

	// Detect "no changes" state. With --stat git emits nothing; with
	// content mode it also emits nothing. Either way, an empty result
	// means a clean tree (or a clean stage in --staged mode).
	if out == "" {
		return c.emptyMessage(staged, filePath), nil
	}

	// Always include a one-line stats header at the top so users have
	// at-a-glance scope context before scrolling content. We compute
	// it separately because asking git for both --stat and content in
	// one call requires --stat-only mode or piping to a parser; a
	// second cheap call is simpler and the perf cost is negligible.
	var header string
	if !statOnly {
		statArgs := append([]string{"diff"}, []string{}...)
		if staged {
			statArgs = append(statArgs, "--cached")
		}
		statArgs = append(statArgs, "--stat")
		if filePath != "" {
			statArgs = append(statArgs, "--", filePath)
		}
		stat, _ := runGitCommand(workDir, statArgs...)
		stat = strings.TrimRight(stat, "\n")
		if stat != "" {
			header = stat + "\n\n"
		}
	}

	body := truncateDiffOutput(out)
	scope := "working tree"
	if staged {
		scope = "staged"
	}
	if filePath != "" {
		scope = filePath
	}

	return fmt.Sprintf("Diff (%s):\n\n%s%s", scope, header, body), nil
}

// emptyMessage tailors the "no changes" copy to whatever the user
// asked for, so `/diff --staged` doesn't claim "working tree is
// clean" when it's the staging area that's empty.
func (c *DiffCommand) emptyMessage(staged bool, filePath string) string {
	switch {
	case filePath != "" && staged:
		return fmt.Sprintf("No staged changes in %s.", filePath)
	case filePath != "":
		return fmt.Sprintf("No changes in %s.", filePath)
	case staged:
		return "Nothing staged. Run `git add <files>` first, or `/diff` to see unstaged changes."
	default:
		return "Working tree is clean."
	}
}

// truncateDiffOutput caps the body to keep the TUI snappy; long
// outputs append a tail telling the user how to see the full thing.
func truncateDiffOutput(s string) string {
	if len(s) <= diffMaxOutputBytes {
		return s
	}
	cut := s[:diffMaxOutputBytes]
	// Cut at the last newline to avoid mid-line slice.
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		cut = cut[:idx]
	}
	return cut + "\n\n... truncated. Run `git diff` directly for the full output."
}

// Compile-time check.
var _ Command = (*DiffCommand)(nil)
