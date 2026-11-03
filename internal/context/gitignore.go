package context

import (
	"os"
	"path/filepath"
	"strings"
)

// gokinGitignoreEntries are paths that should be git-ignored.
var gokinGitignoreEntries = []string{
	".gokin/.session-memory.md",
	".gokin/.working-memory.md",
	".gokin/project-memory.md",
	".gokin/task-output/",
	".gokin/checkpoints/",
	".gokin/learning.yaml",
	".gokin/execution_journal.jsonl",
	".gokin/recovery_snapshot.json",
	".gokin/agent-output/",
	".gokin/agents/checkpoints/",
	"GOKIN.local.md",
}

// EnsureGokinGitignore keeps gokin's runtime files out of `git status`.
//
// The entries go to the repository's local exclude file (.git/info/exclude),
// never to the tracked .gitignore: starting gokin — even a read-only headless
// run — must not leave a modification in the user's repository. Entries the
// user's .gitignore already lists are skipped. Safe to call repeatedly; a
// failure is silent because ignoring runtime files is a convenience.
func EnsureGokinGitignore(workDir string) {
	commonDir := gitCommonDir(workDir)
	if commonDir == "" {
		return
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")

	tracked, _ := os.ReadFile(filepath.Join(workDir, ".gitignore"))
	existing, _ := os.ReadFile(excludePath)
	trackedLines := ignoreLines(string(tracked))
	excludeLines := ignoreLines(string(existing))

	var toAdd []string
	for _, entry := range gokinGitignoreEntries {
		if !trackedLines[entry] && !excludeLines[entry] {
			toAdd = append(toAdd, entry)
		}
	}
	if len(toAdd) == 0 {
		return
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var b strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	if !strings.Contains(string(existing), "# Gokin") {
		b.WriteString("# Gokin working files\n")
	}
	for _, entry := range toAdd {
		b.WriteString(entry + "\n")
	}
	_, _ = f.WriteString(b.String())
}

// ignoreLines returns the set of trimmed, non-comment lines of an ignore file.
func ignoreLines(content string) map[string]bool {
	lines := map[string]bool{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines[line] = true
		}
	}
	return lines
}

// gitCommonDir resolves the git directory that holds info/exclude for a
// repository rooted at workDir, without spawning git. It handles a plain .git
// directory and a linked worktree's `.git` file (gitdir: + commondir).
// It returns "" when workDir is not a repository root.
func gitCommonDir(workDir string) string {
	dotGit := filepath.Join(workDir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return dotGit
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workDir, gitDir)
	}
	if common, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(common))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		return filepath.Clean(c)
	}
	return filepath.Clean(gitDir)
}
