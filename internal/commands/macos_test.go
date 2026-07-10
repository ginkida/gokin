package commands

import (
	"path/filepath"
	"testing"
)

func TestResolveQuickLookPath(t *testing.T) {
	workDir := t.TempDir()

	rel := "dir/space file.go"
	if got, want := resolveQuickLookPath(rel, workDir), filepath.Join(workDir, rel); got != want {
		t.Fatalf("relative path resolved to %q, want %q", got, want)
	}

	abs := filepath.Join(workDir, "already-absolute.go")
	if got := resolveQuickLookPath(abs, workDir); got != abs {
		t.Fatalf("absolute path resolved to %q, want unchanged %q", got, abs)
	}
}
