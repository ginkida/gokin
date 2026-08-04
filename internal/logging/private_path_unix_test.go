//go:build !windows && !plan9

package logging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnablePathLoggingRejectsSymlinkWithoutTouchingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "external.log")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "gokin.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := EnablePathLogging(link, LevelDebug, ""); err == nil {
		DisableLogging()
		t.Fatal("EnablePathLogging accepted a symlink")
	}
	assertLogTargetUntouched(t, target)
}

func TestRotatingWriterFailsClosedWhenPathIsReplaced(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gokin.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("123"); err != nil {
		t.Fatal(err)
	}
	writer := newRotatingFileWriter(file, path, 3, 4)
	t.Cleanup(func() { _ = writer.Close() })

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "external.log")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := writer.Write([]byte("45")); err == nil {
		t.Fatal("rotating writer reported success after its path was replaced")
	}
	assertLogTargetUntouched(t, target)
}

func assertLogTargetUntouched(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("symlink target mode = %04o, want 0644", got)
	}
}
