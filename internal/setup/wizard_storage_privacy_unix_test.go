//go:build !windows && !plan9

package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The wizard writes through a dotfiles user's symlinked config, exactly like
// every other config writer. Refusing would leave the one setup path that
// exists for entering an API key unusable for anyone who symlinks their config
// — and the config layer already keeps the link itself and the target's mode.
func TestSaveProviderConfigWritesThroughSymlink(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.yaml")
	if err := os.WriteFile(target, []byte("keep: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := saveProviderConfig("glm", "new-key", "glm-5.2"); err != nil {
		t.Fatalf("saveProviderConfig refused the user's own symlink: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new-key") {
		t.Fatalf("key was not written through the link: %q", data)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced with a regular file")
	}
	assertWizardMode(t, target, 0o644)
}

func TestSaveProviderConfigRepairsDefaultStorageModes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	dir := filepath.Join(root, "gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("api: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := saveProviderConfig("glm", "new-key", "glm-5.2"); err != nil {
		t.Fatal(err)
	}
	assertWizardMode(t, dir, 0o700)
	assertWizardMode(t, path, 0o600)
}

func assertWizardMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
