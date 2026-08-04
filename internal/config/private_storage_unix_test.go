//go:build !windows && !plan9

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveExplicitConfigPreservesParentMode(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.savePath = filepath.Join(parent, "gokin.yaml")

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertConfigMode(t, parent, 0o755)
	assertConfigMode(t, cfg.savePath, 0o600)
}

func TestSaveProcessBoundExplicitConfigPreservesParentMode(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "bound-config.yaml")
	SetExplicitConfigPath(path)
	t.Cleanup(func() { SetExplicitConfigPath("") })

	cfg := DefaultConfig()
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertConfigMode(t, parent, 0o755)
	assertConfigMode(t, path, 0o600)
}

// A dotfiles user's config is written THROUGH their link, not over it. Refusing
// left them unable to run /login, the setup wizard, or any /set; replacing the
// link (the pre-hardening behaviour) silently orphaned their tracked file.
// Writing to the resolved target keeps both the link and their repository copy.
func TestSaveWritesThroughSymlinkKeepingLinkAndMode(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside.yaml")
	if err := os.WriteFile(target, []byte("keep: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "gokin.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cfg := DefaultConfig()
	cfg.savePath = link
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save refused to write through the user's symlink: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "keep: true\n" || len(data) == 0 {
		t.Fatalf("target was not updated through the link: %q", data)
	}
	// The link survives and the user's own mode is preserved — a mode change
	// would surface as an unexplained diff in their dotfiles repository.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("Save replaced the symlink with a regular file")
	}
	assertConfigMode(t, target, 0o644)
}

// A dangling link has no target to write through, and must say so rather than
// quietly creating a file where the link points.
func TestSaveReportsDanglingSymlinkConfig(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "gokin.yaml")
	if err := os.Symlink(filepath.Join(root, "missing.yaml"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := DefaultConfig()
	cfg.savePath = link
	if err := cfg.Save(); err == nil {
		t.Fatal("Save accepted a dangling symlink")
	}
}

func TestReadExplicitConfigPreservesUserManagedModes(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "repo-config.yaml")
	if err := os.WriteFile(path, []byte("model:\n  name: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfigFile(path); err != nil {
		t.Fatal(err)
	}
	assertConfigMode(t, parent, 0o755)
	assertConfigMode(t, path, 0o644)
}

func TestReadDefaultConfigRepairsLegacyModes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	SetExplicitConfigPath("")
	dir := filepath.Join(root, "gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("model:\n  name: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfigFile(path); err != nil {
		t.Fatal(err)
	}
	assertConfigMode(t, dir, 0o700)
	assertConfigMode(t, path, 0o600)
}

// Symlinking the config into a dotfiles repository is a first-class user
// pattern. Refusing to follow it did not merely skip a file: Gokin failed to
// start with a `configuration` error, so the user's provider and key became
// unreachable on upgrade. The attacker the rejection guards against needs write
// access to the config directory, where replacing config.yaml outright is
// simpler than planting a link — so the trade bought nothing and cost the setup.
//
// The read must still be integrity-checked against the resolved target, and the
// target's own mode must be left alone: it belongs to the user's repository,
// and repairing it would show up as a spurious change there.
func TestReadConfigFollowsSymlinkWithoutChangingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.yaml")
	if err := os.WriteFile(target, []byte("keep: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	read, err := ReadConfigFile(link)
	if err != nil {
		t.Fatalf("ReadConfigFile refused a user's own symlink: %v", err)
	}
	if string(read) != "keep: true\n" {
		t.Fatalf("read through symlink = %q", read)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep: true\n" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertConfigMode(t, target, 0o644)
	// The link itself must stay a link — reading must never replace it.
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("reading replaced the symlink with a regular file")
	}
}

// A symlink whose target is not a regular file is still refused: following it
// would hand config parsing a device, socket, or directory.
func TestReadConfigRejectsSymlinkToNonRegularTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "targetdir")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadConfigFile(link); err == nil {
		t.Fatal("ReadConfigFile accepted a symlink to a directory")
	}
}

// GNU stow and home-manager symlink ~/.config/gokin wholesale. Writing must
// land inside the directory the link points at, and the resolved directory is
// made owner-only because it holds credentials.
func TestWriteDefaultConfigFollowsSymlinkedDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	SetExplicitConfigPath("")
	targetDir := t.TempDir()
	dir := filepath.Join(root, "gokin")
	if err := os.Symlink(targetDir, dir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := WriteConfigFile(path, []byte("keep: false\n")); err != nil {
		t.Fatalf("WriteConfigFile refused a symlinked config directory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(targetDir, "config.yaml"))
	if err != nil {
		t.Fatalf("config was not written into the link target: %v", err)
	}
	if string(data) != "keep: false\n" {
		t.Fatalf("written config = %q", data)
	}
	assertConfigMode(t, targetDir, 0o700)
	assertConfigMode(t, filepath.Join(targetDir, "config.yaml"), 0o600)
}

func assertConfigMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
