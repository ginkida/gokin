package commands

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GrepCommand runs a quick inline search against the working tree.
// Designed for the "where is this string used?" question that comes
// up constantly during code review and pre-commit verification —
// faster + more deterministic than asking the agent to grep.
//
// Backend selection:
//   - In a git repo: `git grep` (respects .gitignore, uses git's
//     index, no need for ripgrep installed)
//   - Otherwise:    not supported (returns a friendly error)
//
// We deliberately don't shell out to `rg` even when present — keeping
// the dependency footprint to "git" matches the rest of the
// inspection commands (/diff, /log, /branches) and avoids surprises
// where output formatting differs between machines.
type GrepCommand struct{}

func (c *GrepCommand) Name() string        { return "grep" }
func (c *GrepCommand) Description() string { return "Search the working tree for a pattern" }
func (c *GrepCommand) Usage() string {
	return `/grep <pattern>           - Search whole tree (case-insensitive)
/grep <pattern> <path>    - Search inside a path (file or dir)
/grep -i <pattern>        - Force case-insensitive (default already)
/grep -w <pattern>        - Whole-word match
/grep -C 2 <pattern>      - With 2 lines of context (max 5)`
}

func (c *GrepCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "grep",
		Priority:    14, // sits with /commit /diff /log /branches
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "<pattern> [path]",
	}
}

const (
	grepMaxOutputBytes = 32 * 1024
	grepMaxContext     = 5
)

func (c *GrepCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	if !isGitRepo(workDir) {
		return "Not a git repository — /grep currently requires git. Use the `grep` tool for general filesystem search.", nil
	}

	if len(args) == 0 {
		return "Usage: /grep <pattern> [path]\n\nExample: /grep TODO\n         /grep \"old func\" internal/", nil
	}

	// Parse flags. We support a tight subset (-i / -w / -C N) so the
	// command stays predictable; for anything more complex the user
	// can shell out to git grep directly.
	caseInsensitive := true // default: case-insensitive (most "where is this?" queries are mixed-case forgiving)
	wholeWord := false
	contextLines := 0
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-i":
			caseInsensitive = true
		case "-w":
			wholeWord = true
		case "-C":
			if i+1 >= len(args) {
				return "Missing value for -C (try /grep -C 2 <pattern>).", nil
			}
			i++
			n := 0
			if _, err := fmt.Sscanf(args[i], "%d", &n); err != nil || n < 0 {
				return fmt.Sprintf("Invalid -C value %q — pass a non-negative integer.", args[i]), nil
			}
			if n > grepMaxContext {
				n = grepMaxContext
			}
			contextLines = n
		default:
			positional = append(positional, a)
		}
	}

	if len(positional) == 0 {
		return "Need a pattern. Example: /grep TODO", nil
	}
	pattern := positional[0]
	var paths []string
	if len(positional) > 1 {
		paths = positional[1:]
	}

	// Compose `git grep -n` (line numbers always on — every result
	// row should be navigable). Other flags layered conditionally.
	gitArgs := []string{"grep", "-n"}
	if caseInsensitive {
		gitArgs = append(gitArgs, "-i")
	}
	if wholeWord {
		gitArgs = append(gitArgs, "-w")
	}
	if contextLines > 0 {
		gitArgs = append(gitArgs, "-C", fmt.Sprintf("%d", contextLines))
	}
	// `--` ends the option list so a pattern starting with `-` doesn't
	// get parsed as a flag.
	gitArgs = append(gitArgs, "--", pattern)
	gitArgs = append(gitArgs, paths...)

	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		// `git grep` exits 1 when no matches found — that's not an
		// error, just an empty result. Surface anything else.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			scope := "working tree"
			if len(paths) > 0 {
				scope = strings.Join(paths, ", ")
			}
			return fmt.Sprintf("No matches for %q in %s.", pattern, scope), nil
		}
		return fmt.Sprintf("git grep failed: %v", err), nil
	}

	body := strings.TrimRight(string(out), "\n")
	if body == "" {
		// Empty output but exit 0 (rare — possibly only matched
		// binary files, which git grep skips by default). Treat as
		// no-match for consistency.
		return fmt.Sprintf("No matches for %q.", pattern), nil
	}

	body = truncateGrepOutput(body)

	scope := "working tree"
	if len(paths) > 0 {
		scope = strings.Join(paths, ", ")
	}
	matchCount := strings.Count(body, "\n") + 1
	header := fmt.Sprintf("Matches for %q (in %s, %d lines):", pattern, scope, matchCount)
	return header + "\n\n" + body + "\n", nil
}

// truncateGrepOutput caps the result so a too-broad pattern doesn't
// flood the scrollback. Cuts at the last newline so we don't slice
// mid-row, then appends a tail telling the user how to refine.
func truncateGrepOutput(s string) string {
	if len(s) <= grepMaxOutputBytes {
		return s
	}
	cut := s[:grepMaxOutputBytes]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		cut = cut[:idx]
	}
	return cut + "\n\n... truncated. Refine the pattern or pass a path: /grep <pattern> <path>."
}

// Compile-time check.
var _ Command = (*GrepCommand)(nil)
