//go:build !windows && !plan9

package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewLoggerRejectsUnsafeStoragePaths(t *testing.T) {
	if _, err := NewLogger(t.TempDir(), "../escape", DefaultConfig()); err == nil {
		t.Fatal("NewLogger accepted path-traversing session ID")
	}

	root := t.TempDir()
	target := filepath.Join(root, "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "audit")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewLogger(root, "session-1", DefaultConfig()); err == nil {
		t.Fatal("NewLogger accepted symlinked audit directory")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("symlink target mode = %04o, want 0755", got)
	}
}
