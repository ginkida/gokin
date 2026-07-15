package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// IsWorkspaceTrusted reports whether workDir exactly matches a persisted trust
// entry. Trust is stored outside the project in the user's main config; a
// repository cannot authorize its own executable hooks.
func IsWorkspaceTrusted(workDir string, trusted []string) bool {
	want, err := canonicalWorkspace(workDir)
	if err != nil || want == "" {
		return false
	}
	for _, candidate := range trusted {
		got, candidateErr := canonicalWorkspace(candidate)
		if candidateErr != nil || got == "" {
			continue
		}
		if runtime.GOOS == "windows" {
			if strings.EqualFold(want, got) {
				return true
			}
		} else if want == got {
			return true
		}
	}
	return false
}

func canonicalWorkspace(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = filepath.Clean(resolved)
	}
	return abs, nil
}
