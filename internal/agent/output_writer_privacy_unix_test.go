//go:build !windows && !plan9

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentOutputWriterCreatesPrivateStorageAndRepairsLegacyFile(t *testing.T) {
	workDir := t.TempDir()
	gokinDir := filepath.Join(workDir, ".gokin")
	outputDir := filepath.Join(gokinDir, "agent-output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(outputDir, "legacy.log")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	writer := NewAgentOutputWriter(workDir, "legacy")
	writer.WriteString("new")
	writer.Close()
	assertAgentOutputMode(t, gokinDir, 0o700)
	assertAgentOutputMode(t, outputDir, 0o700)
	assertAgentOutputMode(t, path, 0o600)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("legacy file was not safely truncated: %q", data)
	}
}

func TestAgentOutputWriterRejectsSymlinkedGokinDirectory(t *testing.T) {
	workDir := t.TempDir()
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(workDir, ".gokin")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writer := NewAgentOutputWriter(workDir, "agent")
	if writer.FilePath() != "" {
		t.Fatalf("writer accepted symlinked .gokin: %s", writer.FilePath())
	}
	writer.WriteString("memory fallback")
	if writer.String() != "memory fallback" {
		t.Fatalf("memory fallback failed: %q", writer.String())
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target directory changed: %v", entries)
	}
}

func TestAgentOutputWriterRejectsSymlinkedOutputDirectory(t *testing.T) {
	workDir := t.TempDir()
	gokinDir := filepath.Join(workDir, ".gokin")
	if err := os.Mkdir(gokinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(gokinDir, "agent-output")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writer := NewAgentOutputWriter(workDir, "agent")
	if writer.FilePath() != "" {
		t.Fatalf("writer accepted symlinked output directory: %s", writer.FilePath())
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target directory changed: %v", entries)
	}
}

func TestAgentOutputWriterRejectsSymlinkedOutputFile(t *testing.T) {
	workDir := t.TempDir()
	outputDir := filepath.Join(workDir, ".gokin", "agent-output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(outputDir, "linked.log")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	writer := NewAgentOutputWriter(workDir, "linked")
	if writer.FilePath() != "" {
		t.Fatalf("writer accepted symlinked output file: %s", writer.FilePath())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertAgentOutputMode(t, target, 0o644)
}

func assertAgentOutputMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
