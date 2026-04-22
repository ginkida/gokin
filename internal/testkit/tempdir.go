package testkit

import (
	"path/filepath"
	"testing"
)

// ResolvedTempDir returns a temp directory with symlinks resolved.
// On macOS, t.TempDir() returns /var/folders/... which is a symlink to
// /private/var/folders/.... PathValidator rejects symlinked roots, so tests
// that feed the returned path to PathValidator must resolve it first.
func ResolvedTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("failed to resolve temp dir symlinks: %v", err)
	}
	return resolved
}
