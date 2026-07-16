package tools

import (
	"fmt"
	"path/filepath"

	"gokin/internal/security"
)

// newWorkspacePathValidator builds the fail-closed filesystem boundary shared
// by workspace-scoped tools. An empty workDir deliberately leaves the
// validator nil: callers must reject execution rather than silently treating
// the process working directory (or the whole filesystem) as trusted.
func newWorkspacePathValidator(workDir string, additionalDirs []string) *security.PathValidator {
	if workDir == "" {
		return nil
	}
	allowed := make([]string, 0, 1+len(additionalDirs))
	allowed = append(allowed, workDir)
	allowed = append(allowed, additionalDirs...)
	return security.NewPathValidator(allowed, false)
}

// validateWorkspacePath resolves relative model-supplied paths against
// workDir, then applies the same symlink-aware containment policy used by the
// read/write tools. The returned path is canonical and safe for direct file
// access.
func validateWorkspacePath(workDir, path string, validator *security.PathValidator) (string, error) {
	if validator == nil || workDir == "" {
		return "", fmt.Errorf("path validator not initialized")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workDir, candidate)
	}
	validated, err := validator.Validate(candidate)
	if err != nil {
		return "", err
	}
	return validated, nil
}

// validateGitPath validates a path before it is handed to git and returns both
// the canonical absolute path and a pathspec relative to the tool's workDir.
// filepath.Rel is intentionally performed only after validation: an unchecked
// absolute sibling otherwise becomes "../secret" and lets git escape a nested
// workspace into the containing repository.
func validateGitPath(workDir, path string, validator *security.PathValidator) (relative, absolute string, err error) {
	absolute, err = validateWorkspacePath(workDir, path, validator)
	if err != nil {
		return "", "", err
	}
	canonicalWorkDir, err := validator.Validate(workDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid working directory: %w", err)
	}
	relative, err = filepath.Rel(canonicalWorkDir, absolute)
	if err != nil {
		return "", "", fmt.Errorf("failed to make path relative to working directory: %w", err)
	}
	return filepath.Clean(relative), absolute, nil
}
