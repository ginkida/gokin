package commands

import (
	"context"
	"fmt"
	"strings"
)

// ShowCommand displays a specific commit's metadata + diff inline.
// Completes the v0.77.x inspect family — answers "what did THIS
// commit do?" the way /log answers "what's been done recently".
//
// Common workflow: /log → spot interesting hash → /show <hash>.
// Without this, users had to copy the hash to the shell or ask the
// agent to invoke `git show`, both an extra step.
//
// Forms:
//
//	/show              - HEAD (the most recent commit)
//	/show <hash>       - Specific commit by short hash
//	/show <ref>        - Any git revision spec (HEAD~3, main, tag/v1)
//	/show <ref> <file> - Diff scoped to one file
type ShowCommand struct{}

func (c *ShowCommand) Name() string        { return "show" }
func (c *ShowCommand) Description() string { return "Show a specific commit (metadata + diff)" }
func (c *ShowCommand) Usage() string {
	return `/show              - Show HEAD
/show <hash>       - Show specific commit
/show HEAD~3       - Three commits ago
/show <ref> <file> - Scoped to one file`
}

func (c *ShowCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{
		Category:    CategoryGit,
		Icon:        "show",
		Priority:    16, // sits with /commit (10) /diff (11) /log (12) /branches (13) /grep (14) /blame (15)
		RequiresGit: true,
		HasArgs:     true,
		ArgHint:     "[ref] [file]",
	}
}

// showMaxOutputBytes mirrors the diff cap — a sprawling commit
// shouldn't dump 50K lines into the TUI scrollback.
const showMaxOutputBytes = 32 * 1024

func (c *ShowCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	workDir := app.GetWorkDir()
	if !isGitRepo(workDir) {
		return "Not a git repository.", nil
	}

	// Parse: first positional is the ref (defaults to HEAD), second is
	// an optional file path scope. Anything beyond is ignored — `git
	// show` accepts more, but the inline UX is cleaner with a hard
	// limit.
	ref := "HEAD"
	var filePath string
	if len(args) > 0 && args[0] != "" {
		ref = args[0]
	}
	if len(args) > 1 && args[1] != "" {
		filePath = args[1]
	}

	gitArgs := []string{
		"show",
		// Relative dates match /log's "3 days ago" style.
		"--date=relative",
		// `--no-color` because we render through the TUI's normal text
		// path which already styles diff blocks via diff_utils.
		"--no-color",
		ref,
	}
	if filePath != "" {
		gitArgs = append(gitArgs, "--", filePath)
	}

	out, err := runGitCommandCtx(ctx, workDir, gitArgs...)
	if err != nil {
		// `git show` fails on bogus ref ("fatal: ambiguous argument
		// 'foo': unknown revision"). The helper returns stderr-as-body
		// on error, so just surface that — it's already informative.
		stderr := strings.TrimSpace(out)
		if stderr == "" {
			stderr = err.Error()
		}
		return fmt.Sprintf("git show failed: %s", stderr), nil
	}

	body := strings.TrimRight(out, "\n")
	if body == "" {
		// Rare — git show on an empty commit (--allow-empty merge)
		// can produce no output. Surface friendly notice.
		return fmt.Sprintf("Commit %s exists but has no displayable content.", ref), nil
	}

	body = truncateShowOutput(body)

	scope := ref
	if filePath != "" {
		scope = fmt.Sprintf("%s — %s", ref, filePath)
	}
	return fmt.Sprintf("Show %s:\n\n%s\n", scope, body), nil
}

// truncateShowOutput caps the body — same shape as truncateDiffOutput.
// A diff-of-everything-in-monorepo shouldn't flood scrollback.
func truncateShowOutput(s string) string {
	if len(s) <= showMaxOutputBytes {
		return s
	}
	cut := s[:showMaxOutputBytes]
	if idx := strings.LastIndexByte(cut, '\n'); idx > 0 {
		cut = cut[:idx]
	}
	return cut + "\n\n... truncated. Run `git show` directly for the full output."
}

// Compile-time check.
var _ Command = (*ShowCommand)(nil)
