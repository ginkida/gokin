package tools

import (
	"path/filepath"
	"testing"
)

// resolvedTempDir returns a temp directory with symlinks resolved.
// On macOS, t.TempDir() returns /var/folders/... which is a symlink to
// /private/var/folders/..., causing PathValidator to reject it.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("failed to resolve temp dir symlinks: %v", err)
	}
	return resolved
}
