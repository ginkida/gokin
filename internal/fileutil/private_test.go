//go:build !windows && !plan9

package fileutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsurePrivateDirCreatesAndRepairsMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir(create): %v", err)
	}
	assertMode(t, dir, 0o700)

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir(repair): %v", err)
	}
	assertMode(t, dir, 0o700)
}

func TestPrivateStorageRejectsSymlinksWithoutChangingTargets(t *testing.T) {
	root := t.TempDir()
	externalDir := filepath.Join(root, "external-dir")
	if err := os.Mkdir(externalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(root, "dir-link")
	if err := os.Symlink(externalDir, dirLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := EnsurePrivateDir(dirLink); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("EnsurePrivateDir symlink error = %v", err)
	}
	assertMode(t, externalDir, 0o755)

	externalFile := filepath.Join(root, "external-file")
	if err := os.WriteFile(externalFile, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(externalFile, 0o644); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(root, "file-link")
	if err := os.Symlink(externalFile, fileLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := SecurePrivateFile(fileLink); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("SecurePrivateFile symlink error = %v", err)
	}
	assertMode(t, externalFile, 0o644)
}

func TestSecurePrivateFileAllowsMissingAndRepairsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SecurePrivateFile(path); err != nil {
		t.Fatalf("SecurePrivateFile(missing): %v", err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SecurePrivateFile(path); err != nil {
		t.Fatalf("SecurePrivateFile(existing): %v", err)
	}
	assertMode(t, path, 0o600)
}

func TestOpenPrivateAppendCreatesRepairsAndAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	file, err := OpenPrivateAppend(path)
	if err != nil {
		t.Fatalf("OpenPrivateAppend(create): %v", err)
	}
	if _, err := file.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	assertMode(t, path, 0o600)

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err = OpenPrivateAppend(path)
	if err != nil {
		t.Fatalf("OpenPrivateAppend(existing): %v", err)
	}
	if _, err := file.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first\nsecond\n" {
		t.Fatalf("appended data = %q", data)
	}
	assertMode(t, path, 0o600)
}

func TestOpenPrivateAppendRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "journal")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if file, err := OpenPrivateAppend(link); err == nil {
		_ = file.Close()
		t.Fatal("OpenPrivateAppend accepted a symlink")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertMode(t, target, 0o644)
}

func TestReadPrivateFileRepairsModeAndEnforcesLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := ReadPrivateFile(path, 7)
	if err != nil || string(data) != "private" {
		t.Fatalf("ReadPrivateFile = %q, %v", data, err)
	}
	assertMode(t, path, 0o600)
	if _, err := ReadPrivateFile(path, 6); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("ReadPrivateFile oversized error = %v", err)
	}
}

func TestReadRegularFilePreservesModeAndRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "repository-config.yaml")
	if err := os.WriteFile(path, []byte("shared"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRegularFile(path, 64)
	if err != nil || string(data) != "shared" {
		t.Fatalf("ReadRegularFile = %q, %v", data, err)
	}
	assertMode(t, path, 0o644)

	link := filepath.Join(root, "config-link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ReadRegularFile(link, 64); err == nil {
		t.Fatal("ReadRegularFile followed a symlink")
	}
	assertMode(t, path, 0o644)
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}

// A directory the USER placed is a different case from Gokin's own state: GNU
// stow and home-manager symlink ~/.config/gokin wholesale, and refusing that
// link meant Gokin could not read the user's config or API keys at all — it
// failed to start. EnsureUserPrivateDir follows it, still verifies that the
// directory it OPENED is the one it inspected, and still makes the resolved
// directory owner-only because it holds credentials.
func TestEnsureUserPrivateDirFollowsSymlinkAndSecuresTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "stowed")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "gokin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := EnsureUserPrivateDir(link); err != nil {
		t.Fatalf("EnsureUserPrivateDir refused the user's own symlink: %v", err)
	}
	assertMode(t, target, 0o700)
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink itself was replaced")
	}
}

// It must still refuse a link that does not resolve to a directory.
func TestEnsureUserPrivateDirRejectsSymlinkToFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "gokin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := EnsureUserPrivateDir(link); err == nil {
		t.Fatal("EnsureUserPrivateDir accepted a symlink to a file")
	}
}
