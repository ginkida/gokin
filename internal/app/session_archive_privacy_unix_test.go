//go:build !windows && !plan9

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionArchiveRepairsLegacyPrivateModes(t *testing.T) {
	workDir := t.TempDir()
	gokinDir := filepath.Join(workDir, ".gokin")
	archiveDir := filepath.Join(gokinDir, "session_archives")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(archiveDir, "session-1.jsonl")
	if err := os.WriteFile(path, []byte("{\"legacy\":true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gokinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(archiveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	application := &App{workDir: workDir}
	if err := application.appendSessionArchive(sessionArchiveRecord{
		Timestamp: time.Now(),
		SessionID: "session-1",
		Reason:    "test",
	}); err != nil {
		t.Fatal(err)
	}
	assertSessionArchiveMode(t, gokinDir, 0o700)
	assertSessionArchiveMode(t, archiveDir, 0o700)
	assertSessionArchiveMode(t, path, 0o600)
}

func TestSessionArchiveRejectsSymlinkedDirectoriesWithoutTouchingTargets(t *testing.T) {
	t.Run("gokin directory", func(t *testing.T) {
		workDir := t.TempDir()
		target := filepath.Join(t.TempDir(), "external")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(workDir, ".gokin")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		application := &App{workDir: workDir}
		err := application.appendSessionArchive(sessionArchiveRecord{SessionID: "session-1"})
		if err == nil {
			t.Fatal("archive accepted a symlinked .gokin directory")
		}
		assertSessionArchiveMode(t, target, 0o755)
	})

	t.Run("archive directory", func(t *testing.T) {
		workDir := t.TempDir()
		gokinDir := filepath.Join(workDir, ".gokin")
		if err := os.Mkdir(gokinDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "external")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(gokinDir, "session_archives")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		application := &App{workDir: workDir}
		err := application.appendSessionArchive(sessionArchiveRecord{SessionID: "session-1"})
		if err == nil {
			t.Fatal("archive accepted a symlinked archive directory")
		}
		assertSessionArchiveMode(t, target, 0o755)
	})
}

func TestSessionArchiveRejectsSymlinkedFilesWithoutTouchingTargets(t *testing.T) {
	workDir := t.TempDir()
	archiveDir, err := prepareSessionArchiveDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(archiveDir, "session-1.jsonl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	application := &App{workDir: workDir}
	if err := application.appendSessionArchive(sessionArchiveRecord{SessionID: "session-1"}); err == nil {
		t.Fatal("archive accepted a symlinked current segment")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	assertSessionArchiveMode(t, target, 0o644)
}

func TestSessionArchiveRejectsSymlinkedRotatedSegmentWithoutTouchingTarget(t *testing.T) {
	workDir := t.TempDir()
	archiveDir, err := prepareSessionArchiveDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	segment := filepath.Join(archiveDir, "session-1.00000000000000000001.jsonl")
	if err := os.Symlink(target, segment); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	application := &App{workDir: workDir}
	if err := application.appendSessionArchive(sessionArchiveRecord{SessionID: "session-1"}); err == nil {
		t.Fatal("archive accepted a symlinked rotated segment")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	assertSessionArchiveMode(t, target, 0o644)
}

func assertSessionArchiveMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
