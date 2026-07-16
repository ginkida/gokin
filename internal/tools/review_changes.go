package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"gokin/internal/security"

	"google.golang.org/genai"
)

// ReviewChangesTool shows a consolidated diff of all uncommitted changes,
// optimized for agent self-verification after a batch of edits.
type ReviewChangesTool struct {
	workDir       string
	pathValidator *security.PathValidator
}

// NewReviewChangesTool creates a new ReviewChangesTool instance.
func NewReviewChangesTool(workDir string) *ReviewChangesTool {
	return &ReviewChangesTool{
		workDir:       workDir,
		pathValidator: newWorkspacePathValidator(workDir, nil),
	}
}

func (t *ReviewChangesTool) Name() string {
	return "review_changes"
}

func (t *ReviewChangesTool) Description() string {
	return "Shows a consolidated view of all uncommitted working-tree changes, including newly-created (untracked) files. " +
		"Use this after making edits to verify what changed before running tests or committing. " +
		"Returns a compact summary: changed files list + diff per file (truncated to first 60 lines each)."
}

// Declaration delegates to the declarations.go copy so the eager and lazy
// registries can never serve divergent schemas (same pattern as mcp_admin).
func (t *ReviewChangesTool) Declaration() *genai.FunctionDeclaration {
	return ReviewChangesToolDeclaration()
}

func (t *ReviewChangesTool) Validate(args map[string]any) error {
	return nil
}

const (
	reviewMaxLinesPerFile = 60
	reviewMaxTotalLines   = 500
	reviewMaxOutputRunes  = 30000
	// reviewMaxNewFileBytes caps how much of an untracked file is read for the
	// 60-line preview — a huge generated artifact must not be slurped whole.
	reviewMaxNewFileBytes = 256 << 10
)

// gitCmd builds a git invocation rooted at workDir. Two flags are load-bearing
// for correctness (pinned in review_changes_test.go):
//   - `-c core.quotepath=off`: with the default quotepath, non-ASCII filenames
//     (файл.go) come back C-quoted with octal escapes — the quoted literal then
//     matches no pathspec (empty per-file diffs) and no real file on disk
//     (untracked files silently vanish).
//   - `--relative` is appended by callers to diff invocations so paths are
//     cwd-relative when workDir is a SUBDIRECTORY of the repo; plain
//     `git diff --name-only` prints repo-root-relative names that would then be
//     re-resolved cwd-relative (empty diffs for every tracked file).
func (t *ReviewChangesTool) gitCmd(ctx context.Context, args ...string) *exec.Cmd {
	// --literal-pathspecs prevents values such as :(top)secret from escaping a
	// nested workspace after their filesystem spelling has passed validation.
	full := append([]string{"-c", "core.quotepath=off", "--literal-pathspecs"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = t.workDir
	return cmd
}

func (t *ReviewChangesTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	staged := GetBoolDefault(args, "staged", false)
	nameOnly := GetBoolDefault(args, "name_only", false)
	file := GetStringDefault(args, "file", "")
	if t.pathValidator == nil {
		return NewErrorResult("security error: path validator not initialized"), nil
	}

	var relFile string
	if file != "" {
		var err error
		relFile, _, err = validateGitPath(t.workDir, file, t.pathValidator)
		if err != nil {
			return NewErrorResult(fmt.Sprintf("file path validation failed: %s", err)), nil
		}
	}

	// --stat header (best-effort; its failure only downgrades the header).
	statArgs := []string{"diff", "--relative", "--stat", "--stat-width=120"}
	if staged {
		statArgs = append(statArgs, "--cached")
	}
	if relFile != "" {
		statArgs = append(statArgs, "--", relFile)
	}
	statOut, statErr := t.gitCmd(ctx, statArgs...).Output()

	// Tracked changed files.
	nameArgs := []string{"diff", "--relative", "--name-only"}
	if staged {
		nameArgs = append(nameArgs, "--cached")
	}
	if relFile != "" {
		nameArgs = append(nameArgs, "--", relFile)
	}
	nameOut, nameErr := t.gitCmd(ctx, nameArgs...).Output()
	trackedRaw := strings.TrimSpace(string(nameOut))
	if nameErr != nil && trackedRaw == "" {
		// Surface git's error instead of silently reporting "clean" — git
		// missing, not a repo, or a locked index must reach the model.
		return NewErrorResult(fmt.Sprintf("review_changes failed: %s", gitErrText(nameErr))), nil
	}

	var tracked []string
	validatedPaths := make(map[string]string)
	if trackedRaw != "" {
		var scopeErr error
		tracked, validatedPaths, scopeErr = t.validateReportedPaths(strings.Split(trackedRaw, "\n"))
		if scopeErr != nil {
			return NewErrorResult(fmt.Sprintf("review_changes rejected git path: %s", scopeErr)), nil
		}
	}

	// Untracked (newly-created) files. `git diff` never shows these, but they
	// are exactly the files an agent just wrote and wants to verify. Only in the
	// working-tree (non-staged) view.
	var untracked []string
	untrackedSet := map[string]bool{}
	if !staged {
		othersArgs := []string{"ls-files", "--others", "--exclude-standard"}
		if relFile != "" {
			othersArgs = append(othersArgs, "--", relFile)
		}
		if othersOut, err := t.gitCmd(ctx, othersArgs...).Output(); err == nil {
			if raw := strings.TrimSpace(string(othersOut)); raw != "" {
				var untrackedPaths map[string]string
				var scopeErr error
				untracked, untrackedPaths, scopeErr = t.validateReportedPaths(strings.Split(raw, "\n"))
				if scopeErr != nil {
					return NewErrorResult(fmt.Sprintf("review_changes rejected git path: %s", scopeErr)), nil
				}
				for _, f := range untracked {
					untrackedSet[f] = true
				}
				for rel, abs := range untrackedPaths {
					validatedPaths[rel] = abs
				}
			}
		}
	}

	if len(tracked) == 0 && len(untracked) == 0 {
		return NewSuccessResult("No uncommitted changes found. Working tree is clean."), nil
	}

	allFiles := make([]string, 0, len(tracked)+len(untracked))
	allFiles = append(allFiles, tracked...)
	allFiles = append(allFiles, untracked...)
	fileCount := len(allFiles)

	var result strings.Builder

	// Header.
	if statErr == nil && strings.TrimSpace(string(statOut)) != "" {
		result.WriteString(strings.TrimSpace(string(statOut)))
		result.WriteString("\n")
	} else {
		fmt.Fprintf(&result, "%d file(s) changed\n", fileCount)
	}
	if len(untracked) > 0 {
		fmt.Fprintf(&result, "(%d new/untracked file(s))\n", len(untracked))
	}

	if nameOnly {
		result.WriteString("\nChanged files:\n")
		for _, f := range allFiles {
			f = strings.TrimSpace(f)
			if untrackedSet[f] {
				fmt.Fprintf(&result, "  %s (new)\n", f)
			} else {
				fmt.Fprintf(&result, "  %s\n", f)
			}
		}
		return NewSuccessResult(result.String()), nil
	}

	// Per-file diff (truncated).
	totalLines := 0
	filesShown := 0
	truncated := false

	for _, f := range allFiles {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if totalLines >= reviewMaxTotalLines {
			truncated = true
			break
		}

		if untrackedSet[f] {
			validPath, ok := validatedPaths[f]
			if !ok {
				return NewErrorResult(fmt.Sprintf("review_changes rejected unvalidated path: %s", f)), nil
			}
			block, n, err := renderUntrackedFile(validPath)
			result.WriteString("\n")
			result.WriteString(strings.Repeat("─", 60))
			if err != nil {
				// A vanished/unreadable file must not silently disappear from a
				// review whose whole purpose is verification.
				fmt.Fprintf(&result, "\n📄 %s (new file — unreadable: %v)\n", f, err)
				totalLines += 2
				filesShown++
				continue
			}
			fmt.Fprintf(&result, "\n📄 %s (new file)\n", f)
			result.WriteString(block)
			totalLines += n
			filesShown++
			continue
		}

		diffArgs := []string{"diff", "--relative"}
		if staged {
			diffArgs = append(diffArgs, "--cached")
		}
		diffArgs = append(diffArgs, "--", f)

		diffOut, err := t.gitCmd(ctx, diffArgs...).Output()
		if err != nil {
			result.WriteString("\n")
			result.WriteString(strings.Repeat("─", 60))
			fmt.Fprintf(&result, "\n📄 %s (diff unavailable: %s)\n", f, gitErrText(err))
			totalLines += 2
			filesShown++
			continue
		}

		diffStr := string(diffOut)
		diffLines := strings.Split(diffStr, "\n")

		result.WriteString("\n")
		result.WriteString(strings.Repeat("─", 60))
		fmt.Fprintf(&result, "\n📄 %s", f)

		if len(diffLines) > reviewMaxLinesPerFile+4 { // +4 for git diff header lines
			show := diffLines[:reviewMaxLinesPerFile+4]
			fmt.Fprintf(&result, "  (%d lines changed, showing first %d)\n", len(diffLines)-4, reviewMaxLinesPerFile)
			result.WriteString(strings.Join(show, "\n"))
			fmt.Fprintf(&result, "\n  ... (%d more lines)", len(diffLines)-reviewMaxLinesPerFile-4)
			totalLines += reviewMaxLinesPerFile + 5
		} else {
			result.WriteString("\n")
			result.WriteString(diffStr)
			totalLines += len(diffLines)
		}
		filesShown++
	}

	if truncated {
		fmt.Fprintf(&result, "\n\n⚠️  Showing diffs for first %d of %d files. Use 'file' parameter to focus.", filesShown, fileCount)
	}

	// Overall output cap (rune-safe).
	if runes := []rune(result.String()); len(runes) > reviewMaxOutputRunes {
		return NewSuccessResult(string(runes[:reviewMaxOutputRunes]) + "\n\n... (output truncated at 30,000 chars)"), nil
	}

	return NewSuccessResult(result.String()), nil
}

// renderUntrackedFile reads at most reviewMaxNewFileBytes of an untracked file
// and renders it via renderNewFileBlock. The byte cap keeps a huge generated
// artifact (dataset, binary) from being slurped whole for a 60-line preview.
func renderUntrackedFile(path string) (string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	content, err := io.ReadAll(io.LimitReader(f, reviewMaxNewFileBytes))
	if err != nil {
		return "", 0, err
	}
	block, n := renderNewFileBlock(string(content), reviewMaxLinesPerFile)
	if info.Size() > reviewMaxNewFileBytes {
		block += fmt.Sprintf("  ... (file is %d bytes; preview capped)\n", info.Size())
		n++
	}
	return block, n, nil
}

// renderNewFileBlock renders an untracked file's content as an all-added block,
// capped at maxLines, returning the rendered text and the number of lines it
// contributes to the overall budget.
func renderNewFileBlock(content string, maxLines int) (string, int) {
	lines := strings.Split(content, "\n")
	// Drop the trailing empty element from a final newline.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	shown := lines
	if total > maxLines {
		shown = lines[:maxLines]
	}
	var b strings.Builder
	for _, l := range shown {
		b.WriteString("+")
		b.WriteString(l)
		b.WriteString("\n")
	}
	if total > maxLines {
		fmt.Fprintf(&b, "  ... (%d more lines)\n", total-maxLines)
	}
	return b.String(), len(shown) + 1
}

// gitErrText extracts the most useful message from a failed git invocation.
func gitErrText(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if s := strings.TrimSpace(string(exitErr.Stderr)); s != "" {
			return s
		}
	}
	return err.Error()
}

// validateReportedPaths applies the same boundary to paths returned by git
// before they are fed into another git command or opened for an untracked-file
// preview. Treating subprocess output as trusted would reintroduce a second,
// less obvious path traversal channel.
func (t *ReviewChangesTool) validateReportedPaths(paths []string) ([]string, map[string]string, error) {
	validated := make([]string, 0, len(paths))
	absoluteByRelative := make(map[string]string, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		relative, absolute, err := validateGitPath(t.workDir, path, t.pathValidator)
		if err != nil {
			return nil, nil, err
		}
		validated = append(validated, relative)
		absoluteByRelative[relative] = absolute
	}
	return validated, absoluteByRelative, nil
}
