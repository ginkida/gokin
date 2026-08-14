package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionJournalUsesTrustedEvalRuntimeDirectory(t *testing.T) {
	workspace := t.TempDir()
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	t.Setenv(evalRuntimeDirEnv, runtimeDir)

	journal, err := newExecutionJournalForWorkDir(workspace)
	if err != nil {
		t.Fatalf("newExecutionJournalForWorkDir: %v", err)
	}
	if err := journal.Append("engine_policy", map[string]any{"mode": "auto"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "execution_journal.jsonl")); err != nil {
		t.Fatalf("trusted runtime journal missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".gokin")); !os.IsNotExist(err) {
		t.Fatalf("eval journal unexpectedly created model-writable workspace storage: %v", err)
	}
}

func TestExecutionJournalRejectsRelativeEvalRuntimeDirectory(t *testing.T) {
	t.Setenv(evalRuntimeDirEnv, "relative/runtime")
	if _, err := newExecutionJournalForWorkDir(t.TempDir()); err == nil {
		t.Fatal("relative trusted runtime directory was accepted")
	}
}
