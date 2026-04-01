package context

import (
	"os"
	"path/filepath"
	"strings"
)

// gokinGitignoreEntries are paths that should be git-ignored.
var gokinGitignoreEntries = []string{
	".gokin/.session-memory.md",
	".gokin/task-output/",
	".gokin/checkpoints/",
	".gokin/learning.yaml",
	"GOKIN.local.md",
}

// EnsureGokinGitignore ensures that .gokin working files are listed in .gitignore.
// It only modifies the file if entries are missing. Safe to call multiple times.
func EnsureGokinGitignore(workDir string) {
	gitignorePath := filepath.Join(workDir, ".gitignore")

	// Only act if this is a git repo (has .git)
	if _, err := os.Stat(filepath.Join(workDir, ".git")); os.IsNotExist(err) {
		return
	}

	existing, _ := os.ReadFile(gitignorePath)
	content := string(existing)

	var toAdd []string
	for _, entry := range gokinGitignoreEntries {
		if !strings.Contains(content, entry) {
			toAdd = append(toAdd, entry)
		}
	}

	if len(toAdd) == 0 {
		return
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	// Add a section header if we're adding to an existing file
	if len(existing) > 0 && !strings.HasSuffix(content, "\n") {
		f.WriteString("\n")
	}
	if !strings.Contains(content, "# Gokin") {
		f.WriteString("\n# Gokin working files\n")
	}
	for _, entry := range toAdd {
		f.WriteString(entry + "\n")
	}
}
