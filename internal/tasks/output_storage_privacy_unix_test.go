//go:build !windows && !plan9

package tasks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTaskOutputCreatesPrivateNestedStorageAndRepairsLegacyFile(t *testing.T) {
	workDir := t.TempDir()
	gokinDir := filepath.Join(workDir, ".gokin")
	outputDir := filepath.Join(gokinDir, "task-output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outputDir, "task_1_1.log")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buffer safeBuffer
	if err := buffer.setTaskOutputFile(workDir, "task_1_1"); err != nil {
		t.Fatal(err)
	}
	_, _ = buffer.Write([]byte("new"))
	buffer.Close()
	assertTaskOutputMode(t, gokinDir, 0o700)
	assertTaskOutputMode(t, outputDir, 0o700)
	assertTaskOutputMode(t, path, 0o600)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("legacy task output was not truncated: %q", data)
	}
}

func TestTaskOutputRejectsSymlinkedStorageComponents(t *testing.T) {
	t.Run("gokin directory", func(t *testing.T) {
		workDir := t.TempDir()
		targetDir := t.TempDir()
		if err := os.Symlink(targetDir, filepath.Join(workDir, ".gokin")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		var buffer safeBuffer
		if err := buffer.setTaskOutputFile(workDir, "task_1_1"); err == nil {
			t.Fatal("accepted symlinked .gokin directory")
		}
		entries, _ := os.ReadDir(targetDir)
		if len(entries) != 0 {
			t.Fatalf("symlink target changed: %v", entries)
		}
	})

	t.Run("output directory", func(t *testing.T) {
		workDir := t.TempDir()
		gokinDir := filepath.Join(workDir, ".gokin")
		if err := os.Mkdir(gokinDir, 0o700); err != nil {
			t.Fatal(err)
		}
		targetDir := t.TempDir()
		if err := os.Symlink(targetDir, filepath.Join(gokinDir, "task-output")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		var buffer safeBuffer
		if err := buffer.setTaskOutputFile(workDir, "task_1_1"); err == nil {
			t.Fatal("accepted symlinked task-output directory")
		}
		entries, _ := os.ReadDir(targetDir)
		if len(entries) != 0 {
			t.Fatalf("symlink target changed: %v", entries)
		}
	})

	t.Run("output file", func(t *testing.T) {
		workDir := t.TempDir()
		outputDir := filepath.Join(workDir, ".gokin", "task-output")
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.log")
		if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(outputDir, "task_1_1.log")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		var buffer safeBuffer
		if err := buffer.setTaskOutputFile(workDir, "task_1_1"); err == nil {
			t.Fatal("accepted symlinked task output file")
		}
		data, _ := os.ReadFile(target)
		if string(data) != "unchanged" {
			t.Fatalf("symlink target changed: %q", data)
		}
		assertTaskOutputMode(t, target, 0o644)
	})
}

func TestSweepRejectsSymlinkedDirectoryAndIgnoresUnmanagedLogs(t *testing.T) {
	workDir := t.TempDir()
	gokinDir := filepath.Join(workDir, ".gokin")
	if err := os.Mkdir(gokinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "task_1_1.log")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(target, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(gokinDir, "task-output")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := sweepStaleTaskOutputFiles(workDir); got != 0 {
		t.Fatalf("sweep through symlink removed %d files", got)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
		t.Fatalf("external stale log changed: %q, %v", data, err)
	}

	if err := os.Remove(filepath.Join(gokinDir, "task-output")); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(gokinDir, "task-output")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unmanaged := filepath.Join(outputDir, "user.log")
	if err := os.WriteFile(unmanaged, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(unmanaged, old, old); err != nil {
		t.Fatal(err)
	}
	if got := sweepStaleTaskOutputFiles(workDir); got != 0 {
		t.Fatalf("sweep removed %d unmanaged logs", got)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("unmanaged log was removed: %v", err)
	}
}

func assertTaskOutputMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
