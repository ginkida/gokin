package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// BlameCommand shows line-by-line authorship for a file via `git blame`.
//
// Completes the v0.77.x git-inspect family (/diff /log /branches /grep)
// — the "who wrote this and when?" question that pairs with /log's
// "what was changed?" view. Without this, users had to drop to the
// shell or ask the agent to run `git blame`, both of which are
// noticeably slower than an inline command.
//
// The output cap (200 lines) is intentional: blame on a 5000-line file
// floods the TUI scrollback faster than the user can scroll, and the
// "show me a window I care about" use case is better served by passing
// a line range. We mirror /grep's truncation tail rather than silently
// drop lines.
type BlameCommand struct{}

func (c *BlameCommand) Name() string        { return "blame" }
func (c *BlameCommand) Description() string { return "Show line-by-line git blame for a file" }
func (c *BlameCommand) Usage() string {
	return `/blame <file>           - Blame entire file (capped at 200 lines)
/blame <file> N         - Single line N
/blame <file> N-M       - Line range N through M
/blame <file> N M       - Line range N through M (alt syntax)`
}

func (c *BlameCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "blame",
		Priority:    15, // sits with /commit (10) /diff (11) /log (12) /branches /grep (14)
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "<file> [N|N-M|N M]",
	}
}

const (
	blameMaxLines = 200
)

func (c *BlameCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	if len(args) == 0 {
		return "Usage: /blame <file> [line-or-range]\n\nExample: /blame internal/app/app.go\n         /blame internal/app/app.go 100-150\n         /blame internal/app/app.go 100", nil
	}

	filePath := args[0]
	startLine, endLine, err := parseBlameRange(args[1:])
	if err != nil {
		return err.Error(), nil
	}

	gitArgs := []string{
		"blame",
		// `--date=relative` matches /log's "3 days ago" style — easier to
		// scan than absolute timestamps in inline output.
		"--date=relative",
		// `-w` ignores whitespace-only changes; almost always the user
		// wants to know who wrote the *content*, not who reflowed it.
		"-w",
	}
	if startLine > 0 {
		// `git blame -L N,M` — both bounds are inclusive. If a single line
		// was requested, M==N which git treats correctly.
		gitArgs = append(gitArgs, "-L", fmt.Sprintf("%d,%d", startLine, endLine))
	}
	gitArgs = append(gitArgs, "--", filePath)

	out, err := runGitCommandCtx(ctx, workDir, gitArgs...)
	if err != nil {
		// `git blame` exits nonzero on bad path / unreadable file.
		// runGitCommandCtx returns stderr as `out` on error — surface it
		// since the message ("fatal: no such path …") is self-explanatory.
		stderr := strings.TrimSpace(out)
		if stderr == "" {
			stderr = err.Error()
		}
		return fmt.Sprintf("git blame failed: %s", stderr), nil
	}

	body := strings.TrimRight(out, "\n")
	if body == "" {
		// Empty file or empty range — friendly notice rather than a blank.
		return fmt.Sprintf("No blame output for %s.", filePath), nil
	}

	body, truncated := truncateBlameOutput(body)

	scope := filePath
	if startLine > 0 {
		if startLine == endLine {
			scope = fmt.Sprintf("%s:%d", filePath, startLine)
		} else {
			scope = fmt.Sprintf("%s:%d-%d", filePath, startLine, endLine)
		}
	}

	header := fmt.Sprintf("Blame %s:\n\n", scope)
	if truncated {
		return header + body + fmt.Sprintf("\n\n... truncated at %d lines. Pass a range: /blame %s N-M.\n", blameMaxLines, filePath), nil
	}
	return header + body + "\n", nil
}

// parseBlameRange handles the three accepted shapes after the file
// argument: nothing, a single int "N", a range "N-M", or two ints
// "N M". Returns startLine/endLine where 0 means "blame entire file".
func parseBlameRange(rest []string) (start, end int, err error) {
	if len(rest) == 0 {
		return 0, 0, nil
	}

	// "N-M" form (no whitespace between bounds).
	if len(rest) == 1 && strings.Contains(rest[0], "-") {
		parts := strings.SplitN(rest[0], "-", 2)
		s, sErr := strconv.Atoi(parts[0])
		e, eErr := strconv.Atoi(parts[1])
		if sErr != nil || eErr != nil || s < 1 || e < s {
			return 0, 0, fmt.Errorf("invalid range %q — use N-M with positive integers, M >= N", rest[0])
		}
		return s, e, nil
	}

	// "N" form — single line.
	if len(rest) == 1 {
		n, nErr := strconv.Atoi(rest[0])
		if nErr != nil || n < 1 {
			return 0, 0, fmt.Errorf("invalid line number %q — pass a positive integer", rest[0])
		}
		return n, n, nil
	}

	// "N M" form — two integers.
	if len(rest) == 2 {
		s, sErr := strconv.Atoi(rest[0])
		e, eErr := strconv.Atoi(rest[1])
		if sErr != nil || eErr != nil || s < 1 || e < s {
			return 0, 0, fmt.Errorf("invalid range %q %q — use positive integers, second >= first", rest[0], rest[1])
		}
		return s, e, nil
	}

	return 0, 0, fmt.Errorf("too many arguments; use /blame <file> [N|N-M|N M]")
}

// truncateBlameOutput caps the line count so a 5000-line blame doesn't
// flood the scrollback. Returns the (possibly trimmed) body and a flag
// indicating whether truncation happened, so the caller can append a
// "pass a range" hint.
func truncateBlameOutput(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	if len(lines) <= blameMaxLines {
		return body, false
	}
	return strings.Join(lines[:blameMaxLines], "\n"), true
}

// Compile-time check.
var _ Command = (*BlameCommand)(nil)
