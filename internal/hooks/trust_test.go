package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsWorkspaceTrustedRequiresExactCanonicalRoot(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsWorkspaceTrusted(root, []string{root}) {
		t.Fatal("exact workspace trust entry was not accepted")
	}
	if IsWorkspaceTrusted(child, []string{root}) {
		t.Fatal("parent trust must not silently authorize nested repositories")
	}
	if IsWorkspaceTrusted(root, nil) {
		t.Fatal("empty trust list authorized project hooks")
	}
}

func TestIsWorkspaceTrustedResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if !IsWorkspaceTrusted(link, []string{root}) {
		t.Fatal("canonical workspace identity should survive a symlinked cwd")
	}
}
